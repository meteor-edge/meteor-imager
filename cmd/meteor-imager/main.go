// Command meteor-imager writes Raspberry Pi OS images to SD cards and sets up
// Wi-Fi and MeteorCloud enrollment on the card's boot partition.
//
// With no arguments it opens the desktop UI; subcommands are the CLI.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	_ "time/tzdata" // timezone validation on systems without zoneinfo (Windows)

	"golang.org/x/term"

	"github.com/meteor-edge/meteor-imager/internal/devices"
	"github.com/meteor-edge/meteor-imager/internal/flash"
	"github.com/meteor-edge/meteor-imager/internal/gui"
	"github.com/meteor-edge/meteor-imager/internal/job"
	"github.com/meteor-edge/meteor-imager/internal/meteorcloud"
	"github.com/meteor-edge/meteor-imager/internal/profile"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `Meteor Imager writes Raspberry Pi OS to SD cards, with Wi-Fi and MeteorCloud enrollment.

Usage:
  meteor-imager                      open the desktop UI
  meteor-imager <command> [flags]

Commands:
  gui            open the desktop UI (default)
  images         list OS images a MeteorCloud server publishes
  download       download a published image to the local cache
  list-devices   list cards that can be written
  flash          write an image to a card, verify it, apply a profile, eject
  configure      write a configured copy of an image to a new .img file
  profile        print an example FlashProfile JSON
  version        print the version

Run "meteor-imager <command> -h" for a command's flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	args := os.Args[1:]
	cmd := "gui"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "gui":
		err = runGUI(ctx, args)
	case "images":
		err = runImages(ctx, args)
	case "download":
		err = runDownload(ctx, args)
	case "list-devices":
		err = runListDevices(ctx, args)
	case "flash":
		err = runFlash(ctx, args)
	case "configure":
		err = runConfigure(ctx, args)
	case "profile":
		err = printExampleProfile(os.Stdout)
	case "version":
		fmt.Println("meteor-imager", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runGUI(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("gui", flag.ContinueOnError)
	noBrowser := fs.Bool("no-browser", false, "print the address instead of opening a browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return gui.Run(ctx, gui.Options{Version: version, OpenBrowser: !*noBrowser})
}

// serverFlags are shared by commands that talk to MeteorCloud.
type serverFlags struct {
	server   *string
	insecure *bool
}

func addServerFlags(fs *flag.FlagSet) serverFlags {
	return serverFlags{
		server:   fs.String("server", os.Getenv("METEOR_IMAGER_SERVER"), "MeteorCloud address, e.g. fleet.example.com (env METEOR_IMAGER_SERVER)"),
		insecure: fs.Bool("insecure-http", false, "allow plain HTTP to a non-LAN server (dev only)"),
	}
}

func (s serverFlags) client() (*meteorcloud.Client, error) {
	if *s.server == "" {
		return nil, errors.New("--server is required")
	}
	u, err := meteorcloud.ParseServer(*s.server, *s.insecure)
	if err != nil {
		return nil, err
	}
	return meteorcloud.New(u, version), nil
}

func runImages(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("images", flag.ContinueOnError)
	sf := addServerFlags(fs)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := sf.client()
	if err != nil {
		return err
	}
	images, err := c.ListImages(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(os.Stdout, images)
	}
	if len(images) == 0 {
		fmt.Println("This server publishes no OS images yet. In MeteorCloud, tick \"Show in Meteor Imager\" on an OS image.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tVERSION\tDEVICE TYPE\tSIZE")
	for _, im := range images {
		dt := ""
		if im.DeviceType != nil {
			dt = *im.DeviceType
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", im.ID, im.Name, im.Version, dt, devices.HumanSize(im.SizeBytes))
	}
	return tw.Flush()
}

func findImage(ctx context.Context, c *meteorcloud.Client, ref string) (*meteorcloud.Image, error) {
	images, err := c.ListImages(ctx)
	if err != nil {
		return nil, err
	}
	var matches []meteorcloud.Image
	for _, im := range images {
		if im.ID == ref || im.Name+"@"+im.Version == ref || (im.Name == ref) {
			matches = append(matches, im)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no published image %q; run \"meteor-imager images\"", ref)
	case 1:
		return &matches[0], nil
	default:
		// Name only: newest first, as the server sorts.
		if matches[0].ID != ref && !strings.Contains(ref, "@") {
			return &matches[0], nil
		}
		return nil, fmt.Errorf("%q matches more than one image; use its ID", ref)
	}
}

func runDownload(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("download", flag.ContinueOnError)
	sf := addServerFlags(fs)
	ref := fs.String("image", "", "image ID, name, or name@version")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := sf.client()
	if err != nil {
		return err
	}
	im, err := findImage(ctx, c, *ref)
	if err != nil {
		return err
	}
	cache, err := meteorcloud.CacheDir()
	if err != nil {
		return err
	}
	bar := newBar("download")
	p, err := c.Download(ctx, *im, cache, func(done, total int64) {
		if total > 0 {
			bar.set(float64(done) / float64(total))
		}
	})
	bar.done()
	if err != nil {
		return err
	}
	fmt.Println(p)
	return nil
}

func runListDevices(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("list-devices", flag.ContinueOnError)
	all := fs.Bool("all", false, "also show internal and system disks (they still cannot be written)")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	devs, err := devices.List(ctx, *all)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(os.Stdout, devs)
	}
	if len(devs) == 0 {
		fmt.Println("No removable cards found. Insert a card (use --all to see every disk).")
		return nil
	}
	for _, d := range devs {
		note := ""
		if !d.Safe() {
			note = "  [not writable: system, internal or read-only]"
		}
		fmt.Println(d.Describe() + note)
	}
	return nil
}

// profileFlags build a FlashProfile from a JSON file and/or flags. Secrets
// come from the file, the environment, or a hidden prompt; never from flags,
// which other users can see in the process list.
type profileFlags struct {
	file, ssid, country, domain, hostname, timezone *string
	hidden, noWiFi, noEnroll                        *bool
}

func addProfileFlags(fs *flag.FlagSet) profileFlags {
	return profileFlags{
		file:     fs.String("profile", "", "FlashProfile JSON file (see \"meteor-imager profile\")"),
		ssid:     fs.String("wifi-ssid", "", "Wi-Fi network name; password from METEOR_WIFI_PSK or a prompt"),
		country:  fs.String("wifi-country", "", "Wi-Fi country code, e.g. DE"),
		hidden:   fs.Bool("wifi-hidden", false, "the Wi-Fi network is hidden"),
		domain:   fs.String("meteor-domain", "", "MeteorCloud address the Pi reaches (METEOR_DOMAIN); key from METEOR_API_KEY or a prompt"),
		hostname: fs.String("hostname", "", "hostname for the Pi"),
		timezone: fs.String("timezone", "", "IANA timezone, e.g. Europe/Berlin"),
		noWiFi:   fs.Bool("no-wifi", false, "do not set up Wi-Fi"),
		noEnroll: fs.Bool("no-enrollment", false, "do not write MeteorCloud enrollment"),
	}
}

func (pf profileFlags) build() (*profile.FlashProfile, error) {
	var p *profile.FlashProfile
	if *pf.file != "" {
		f, err := os.Open(*pf.file)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if p, err = profile.Parse(f); err != nil {
			return nil, err
		}
	} else {
		p = &profile.FlashProfile{SchemaVersion: profile.SchemaVersion}
	}
	if *pf.ssid != "" {
		psk, err := secret("METEOR_WIFI_PSK", "Wi-Fi password for "+*pf.ssid)
		if err != nil {
			return nil, err
		}
		p.WiFi = &profile.WiFi{SSID: *pf.ssid, PSK: psk, Hidden: *pf.hidden, Country: strings.ToUpper(*pf.country)}
	}
	if *pf.domain != "" {
		key, err := secret("METEOR_API_KEY", "MeteorCloud enrollment key")
		if err != nil {
			return nil, err
		}
		p.Enrollment = &profile.Enrollment{MeteorDomain: *pf.domain, MeteorAPIKey: key}
	}
	if *pf.hostname != "" {
		p.Hostname = pf.hostname
	}
	if *pf.timezone != "" {
		p.Timezone = pf.timezone
	}
	if *pf.noWiFi {
		p.WiFi = nil
	}
	if *pf.noEnroll {
		p.Enrollment = nil
	}
	if p.WiFi == nil && p.Enrollment == nil && p.Hostname == nil && p.Timezone == nil {
		return nil, nil
	}
	return p, p.Validate()
}

func secret(env, prompt string) (string, error) {
	if v := os.Getenv(env); v != "" {
		return v, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("set %s or run in a terminal to be prompted", env)
	}
	fmt.Fprintf(os.Stderr, "%s: ", prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return strings.TrimRight(string(b), "\r\n"), err
}

func runFlash(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("flash", flag.ContinueOnError)
	sf := addServerFlags(fs)
	pf := addProfileFlags(fs)
	image := fs.String("image", "", "local image file (.img, .img.xz, .gz, .zip), or with --server a published image ID or name")
	device := fs.String("device", "", "card to write, e.g. /dev/sdb, /dev/disk4, \\\\.\\PhysicalDrive2 (see list-devices)")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	noVerify := fs.Bool("no-verify", false, "skip reading the card back")
	noEject := fs.Bool("no-eject", false, "leave the card connected")
	nonRemovable := fs.Bool("allow-non-removable", false, "allow a writable disk that is not marked removable (never a system disk)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *image == "" || *device == "" {
		return errors.New("--image and --device are required")
	}
	if ok, why := flash.CanWriteDevices(); !ok {
		return errors.New(why)
	}
	p, err := pf.build()
	if err != nil {
		return err
	}

	var src job.Source
	if *sf.server != "" {
		c, err := sf.client()
		if err != nil {
			return err
		}
		im, err := findImage(ctx, c, *image)
		if err != nil {
			return err
		}
		src = job.Source{Client: c, Image: im}
	} else {
		if _, err := os.Stat(*image); err != nil {
			return err
		}
		src = job.Source{Path: *image}
	}

	d, err := devices.Find(ctx, *device)
	if err != nil {
		return err
	}
	if err := job.CheckDevice(d, *nonRemovable); err != nil {
		return err
	}
	if !*yes {
		fmt.Fprintf(os.Stderr, "\nEVERYTHING on this disk will be erased:\n  %s\n\n", d.Describe())
		if p != nil && p.WiFi != nil {
			fmt.Fprintln(os.Stderr, "Note: the Wi-Fi password is stored on the card's boot partition; anyone with the card can read it.")
		}
		fmt.Fprintf(os.Stderr, "Type %q to continue: ", d.Name)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(line) != d.Name {
			return errors.New("cancelled")
		}
	}

	bars := map[string]*bar{}
	rep, err := job.Run(ctx, job.Request{
		Source: src, Device: d, Profile: p,
		AllowNonRemovable: *nonRemovable, SkipVerify: *noVerify, NoEject: *noEject,
	}, func(e job.Event) {
		if e.Message != "" && e.Stage != job.StageOpen {
			fmt.Fprintln(os.Stderr, e.Message)
		}
		switch e.Stage {
		case job.StageDownload, job.StageWrite, job.StageVerify:
			b := bars[e.Stage]
			if b == nil {
				b = newBar(e.Stage)
				bars[e.Stage] = b
			}
			b.set(e.Fraction)
			if e.Fraction >= 1 {
				b.done()
			}
		}
	})
	for _, b := range bars {
		b.done()
	}
	if err != nil {
		return err
	}
	printReport(rep)
	return nil
}

func runConfigure(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("configure", flag.ContinueOnError)
	sf := addServerFlags(fs)
	pf := addProfileFlags(fs)
	image := fs.String("image", "", "source image file, or with --server a published image ID or name")
	out := fs.String("out", "", "new .img file to write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *image == "" || *out == "" {
		return errors.New("--image and --out are required")
	}
	p, err := pf.build()
	if err != nil {
		return err
	}
	if p == nil {
		return errors.New("nothing to configure: give --profile or Wi-Fi/enrollment flags")
	}
	defer p.Clear()
	path := *image
	if *sf.server != "" {
		c, err := sf.client()
		if err != nil {
			return err
		}
		im, err := findImage(ctx, c, *image)
		if err != nil {
			return err
		}
		cache, err := meteorcloud.CacheDir()
		if err != nil {
			return err
		}
		b := newBar("download")
		path, err = c.Download(ctx, *im, cache, func(done, total int64) {
			if total > 0 {
				b.set(float64(done) / float64(total))
			}
		})
		b.done()
		if err != nil {
			return err
		}
	}
	b := newBar("write")
	rep, err := flash.ConfigureFile(ctx, path, *out, p, flash.Options{
		Progress: func(s flash.Stage, f float64) {
			if s == flash.StageWrite {
				b.set(f)
			}
		},
	})
	b.done()
	if err != nil {
		return err
	}
	printReport(rep)
	fmt.Fprintln(os.Stderr, "Wrote", *out)
	return nil
}

func printReport(rep *flash.Report) {
	fmt.Fprintf(os.Stderr, "Wrote %s", devices.HumanSize(rep.BytesWritten))
	if rep.Verified {
		fmt.Fprint(os.Stderr, ", verified")
	}
	fmt.Fprintln(os.Stderr, ".")
	if rep.Boot != nil {
		if len(rep.Boot.Written) > 0 {
			fmt.Fprintln(os.Stderr, "Boot partition:", strings.Join(rep.Boot.Written, ", "))
		}
		for _, w := range rep.Boot.Warnings {
			fmt.Fprintln(os.Stderr, "Warning:", w)
		}
	}
}

func printExampleProfile(w io.Writer) error {
	host, tz := "pi-01", "Europe/Berlin"
	return writeJSON(w, profile.FlashProfile{
		SchemaVersion: profile.SchemaVersion,
		WiFi:          &profile.WiFi{SSID: "MyNetwork", PSK: "wifi-password", Country: "DE"},
		Enrollment:    &profile.Enrollment{MeteorDomain: "fleet.example.com", MeteorAPIKey: "key_..."},
		Hostname:      &host,
		Timezone:      &tz,
	})
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// bar is a one-line progress bar on stderr.
type bar struct {
	label string
	last  int
	tty   bool
	shown bool
}

func newBar(label string) *bar {
	return &bar{label: label, last: -1, tty: term.IsTerminal(int(os.Stderr.Fd()))}
}

func (b *bar) set(f float64) {
	pct := int(f * 100)
	if pct == b.last {
		return
	}
	b.last, b.shown = pct, true
	if b.tty {
		n := pct * 30 / 100
		fmt.Fprintf(os.Stderr, "\r%-9s [%s%s] %3d%%", b.label, strings.Repeat("#", n), strings.Repeat(".", 30-n), pct)
	} else if pct%10 == 0 {
		fmt.Fprintf(os.Stderr, "%s %d%%\n", b.label, pct)
	}
}

func (b *bar) done() {
	if b.shown && b.tty {
		fmt.Fprintln(os.Stderr)
	}
	b.shown = false
}
