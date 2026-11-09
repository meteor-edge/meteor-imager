// Package job runs one end-to-end card write (download if needed, write,
// verify, configure, eject) for both the CLI and the desktop UI.
package job

import (
	"context"
	"errors"
	"fmt"

	"github.com/meteor-edge/meteor-imager/internal/devices"
	"github.com/meteor-edge/meteor-imager/internal/diskimage"
	"github.com/meteor-edge/meteor-imager/internal/flash"
	"github.com/meteor-edge/meteor-imager/internal/meteorcloud"
	"github.com/meteor-edge/meteor-imager/internal/profile"
)

// Source is either a local image file or a published image on a server.
type Source struct {
	Path   string
	Client *meteorcloud.Client
	Image  *meteorcloud.Image
}

// Request is one card write.
type Request struct {
	Source  Source
	Device  devices.Device
	Profile *profile.FlashProfile // nil: write the image unchanged
	// AllowNonRemovable allows a writable non-system disk that is not marked
	// removable (some card readers). System disks are always refused.
	AllowNonRemovable bool
	SkipVerify        bool
	NoEject           bool
}

// Event is a progress update. Fraction is 0..1 within the stage.
type Event struct {
	Stage    string  `json:"stage"`
	Fraction float64 `json:"fraction"`
	// Bytes written (write) or read back (verify) so far.
	Bytes   int64  `json:"bytes,omitempty"`
	Message string `json:"message,omitempty"`
}

// Stages, in order.
const (
	StageDownload  = "download"
	StageOpen      = "open"
	StageWrite     = string(flash.StageWrite)
	StageVerify    = string(flash.StageVerify)
	StageConfigure = string(flash.StageConfigure)
	StageFinish    = string(flash.StageFinish)
	StageEject     = "eject"
)

// CheckDevice refuses disks that must never be written.
func CheckDevice(d devices.Device, allowNonRemovable bool) error {
	switch {
	case d.System:
		return fmt.Errorf("%s holds the running system and will not be written", d.Path)
	case d.ReadOnly:
		return fmt.Errorf("%s is read-only (check the card's lock switch)", d.Path)
	case d.SizeBytes <= 0:
		return fmt.Errorf("%s has no media; insert a card", d.Path)
	case !d.Removable && !allowNonRemovable:
		return fmt.Errorf("%s is not a removable disk; refusing to write it", d.Path)
	}
	return nil
}

// Run performs the request. The profile's secrets are cleared when it returns.
func Run(ctx context.Context, req Request, emit func(Event)) (*flash.Report, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	if req.Profile != nil {
		defer req.Profile.Clear()
		if err := req.Profile.Validate(); err != nil {
			return nil, err
		}
	}
	if err := CheckDevice(req.Device, req.AllowNonRemovable); err != nil {
		return nil, err
	}

	path := req.Source.Path
	if path == "" {
		if req.Source.Client == nil || req.Source.Image == nil {
			return nil, errors.New("no image selected")
		}
		cache, err := meteorcloud.CacheDir()
		if err != nil {
			return nil, err
		}
		emit(Event{Stage: StageDownload, Message: req.Source.Image.Name + " " + req.Source.Image.Version})
		path, err = req.Source.Client.Download(ctx, *req.Source.Image, cache, func(done, total int64) {
			if total > 0 {
				emit(Event{Stage: StageDownload, Fraction: float64(done) / float64(total)})
			}
		})
		if err != nil {
			return nil, err
		}
	}

	src, err := diskimage.Open(path)
	if err != nil {
		return nil, err
	}
	defer src.Close()

	emit(Event{Stage: StageOpen, Message: req.Device.Describe()})
	tgt, err := flash.OpenDevice(ctx, req.Device)
	if err != nil {
		return nil, err
	}
	rep, err := flash.Write(ctx, src, tgt, req.Device.SizeBytes, req.Profile, flash.Options{
		SkipVerify: req.SkipVerify,
		SyncEvery:  flash.DeviceSyncEvery,
		Progress:   func(s flash.Stage, f float64, n int64) { emit(Event{Stage: string(s), Fraction: f, Bytes: n}) },
	})
	if cerr := tgt.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("close %s: %w", req.Device.Path, cerr)
	}
	if err != nil {
		return nil, err
	}
	if !req.NoEject {
		emit(Event{Stage: StageEject})
		if err := flash.Eject(ctx, req.Device); err != nil {
			// The data is safe on the card; only the eject failed.
			emit(Event{Stage: StageEject, Fraction: 1, Message: "Could not eject automatically (" + err.Error() + "); eject the card before removing it."})
			return rep, nil
		}
	}
	emit(Event{Stage: StageEject, Fraction: 1})
	return rep, nil
}
