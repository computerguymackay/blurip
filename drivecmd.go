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
)

const driveUsage = `Usage: blurip drive <status|watch|eject> [device ...]

  status   Show each drive's status
  watch    Print status changes and newly ready discs until Ctrl-C
  eject    Eject the disc and wait until the drive reports it is gone

With no devices, all /dev/sr* drives are used.
`

func runDrive(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "help" {
		fmt.Fprint(os.Stderr, driveUsage)
		return nil
	}
	sub, devices := args[0], args[1:]
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
		fmt.Fprintln(tw, "DEVICE\tSTATUS")
		for _, dev := range devices {
			st, err := ctl.Status(dev)
			msg := st.String()
			if err != nil {
				msg = "error: " + err.Error()
			}
			fmt.Fprintf(tw, "%s\t%s\n", dev, msg)
		}
		return tw.Flush()

	case "watch":
		return watchDrives(ctx, ctl, devices)

	case "eject":
		for _, dev := range devices {
			start := time.Now()
			fmt.Printf("Ejecting %s... ", dev)
			if err := drive.EjectAndConfirm(ctx, ctl, dev, drive.EjectOptions{}); err != nil {
				fmt.Println("failed")
				return err
			}
			fmt.Printf("gone (%s)\n", time.Since(start).Round(100*time.Millisecond))
		}
		return nil
	}
	fmt.Fprint(os.Stderr, driveUsage)
	return fmt.Errorf("unknown drive command %q", sub)
}

// watchDrives prints status changes and uses a Watcher the way the ripper
// will: each ready disc is reported once, then ignored until removed.
func watchDrives(ctx context.Context, ctl drive.Controller, devices []string) error {
	fmt.Printf("Watching %s (Ctrl-C to stop)\n", strings.Join(devices, ", "))
	w := drive.NewWatcher(ctl, time.Second)
	w.OnError = func(dev string, err error) { fmt.Printf("%s  %s  error: %v\n", stamp(), dev, err) }
	last := map[string]drive.Status{}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		for _, dev := range devices {
			if st, err := ctl.Status(dev); err == nil {
				if prev, seen := last[dev]; !seen || prev != st {
					fmt.Printf("%s  %s  %s\n", stamp(), dev, st)
					last[dev] = st
				}
			}
		}
		for _, dev := range w.Poll(devices) {
			fmt.Printf("%s  %s  NEW DISC READY (would rip now; ignored until removed)\n", stamp(), dev)
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
