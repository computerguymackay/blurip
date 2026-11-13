package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/computerguymackay/blurip/internal/drive"
	"github.com/computerguymackay/blurip/internal/store"
)

const driveUsage = `Usage: blurip drive <status|watch|eject> [drive ...]

  status   Show each drive's status
  watch    Print status changes and newly ready discs until Ctrl-C
  eject    Eject the disc and wait until the drive reports it is gone,
           showing every step (for diagnosing a drive that won't eject)

A drive is /dev/sr1, sr1 or its nickname. With none, all /dev/sr* drives
are used.
`

func runDrive(dbPath string, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "help" {
		fmt.Fprint(os.Stderr, driveUsage)
		return nil
	}
	sub, refs := args[0], args[1:]
	known := savedDrives(dbPath)
	var devices []string
	for _, ref := range refs {
		dev, err := resolveDrive(ref, known)
		if err != nil {
			return err
		}
		devices = append(devices, dev)
	}
	label := func(dev string) string {
		for _, d := range known {
			if d.Device == dev {
				return d.Label()
			}
		}
		return shortDev(dev)
	}
	if len(devices) == 0 {
		var err error
		if devices, err = drive.Discover(); err != nil {
			return err
		}
		if len(devices) == 0 {
			return errors.New("no optical drives found (/dev/sr*)")
		}
	}
	ctl := drive.CDROM{UseEjectCommand: true}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	switch sub {
	case "status":
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "DRIVE\tSTATUS")
		for _, dev := range devices {
			st, err := ctl.Status(dev)
			msg := st.String()
			if err != nil {
				msg = "error: " + err.Error()
			}
			fmt.Fprintf(tw, "%s\t%s\n", label(dev), msg)
		}
		return tw.Flush()

	case "watch":
		return watchDrives(ctx, ctl, devices, label)

	case "eject":
		for _, dev := range devices {
			start := time.Now()
			trace := func(format string, a ...any) {
				fmt.Printf("  %5.1fs  %s\n", time.Since(start).Seconds(), fmt.Sprintf(format, a...))
			}
			ctl.Trace = trace
			fmt.Printf("Ejecting %s:\n", label(dev))
			if err := drive.EjectAndConfirm(ctx, ctl, dev, drive.EjectOptions{Trace: trace}); err != nil {
				return err
			}
			fmt.Printf("Ejected %s in %s\n", label(dev), time.Since(start).Round(100*time.Millisecond))
		}
		return nil
	}
	fmt.Fprint(os.Stderr, driveUsage)
	return fmt.Errorf("unknown drive command %q", sub)
}

// watchDrives prints status changes and uses a Watcher the way the ripper
// will: each ready disc is reported once, then ignored until removed.
func watchDrives(ctx context.Context, ctl drive.Controller, devices []string, label func(string) string) error {
	names := make([]string, len(devices))
	for i, d := range devices {
		names[i] = label(d)
	}
	fmt.Printf("Watching %s (Ctrl-C to stop)\n", strings.Join(names, ", "))
	w := drive.NewWatcher(ctl, time.Second)
	w.OnError = func(dev string, err error) { fmt.Printf("%s  %s  error: %v\n", stamp(), label(dev), err) }
	last := map[string]drive.Status{}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		for _, dev := range devices {
			if st, err := ctl.Status(dev); err == nil {
				if prev, seen := last[dev]; !seen || prev != st {
					fmt.Printf("%s  %s  %s\n", stamp(), label(dev), st)
					last[dev] = st
				}
			}
		}
		for _, dev := range w.Poll(devices) {
			fmt.Printf("%s  %s  NEW DISC READY (would rip now; ignored until removed)\n", stamp(), label(dev))
			w.Ignore(dev)
		}
		select {
		case <-ctx.Done():
			fmt.Println()
			return nil
		case <-t.C:
		}
	}
}

func stamp() string { return time.Now().Format("15:04:05") }

// savedDrives returns the drives saved in the database, or nil if there is
// no database yet (the drive commands work without setup).
func savedDrives(dbPath string) []store.Drive {
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		return nil
	}
	defer s.Close()
	drives, _ := s.Drives(context.Background())
	return drives
}

// resolveDrive turns /dev/sr1, sr1 or a nickname into a device path.
func resolveDrive(ref string, known []store.Drive) (string, error) {
	if strings.HasPrefix(ref, "/") {
		return ref, nil
	}
	for _, d := range known {
		if d.Matches(ref) {
			return d.Device, nil
		}
	}
	if strings.HasPrefix(ref, "sr") {
		return "/dev/" + ref, nil
	}
	return "", fmt.Errorf("no drive called %q; run `blurip config` to list the drives", ref)
}
