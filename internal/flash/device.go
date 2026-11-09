package flash

import (
	"context"

	"github.com/meteor-edge/meteor-imager/internal/devices"
)

// OpenDevice unmounts the card's volumes and opens it for raw read/write,
// asking the OS for permission where needed (polkit on Linux, an
// administrator prompt on macOS; Windows needs an elevated process).
func OpenDevice(ctx context.Context, d devices.Device) (Target, error) { return openDevice(ctx, d) }

// Eject flushes and ejects the card so it can be removed safely.
func Eject(ctx context.Context, d devices.Device) error { return eject(ctx, d) }

// CanWriteDevices reports whether this process can write cards, and if not why.
func CanWriteDevices() (bool, string) { return canWriteDevices() }
