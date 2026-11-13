package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/drive"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/store"
	"github.com/dustin/go-humanize"
)

// startup is what blurip shows before it starts watching: the drives it
// found, a summary of past rips, and (when asking) how to use the drives
// this time.
type startup struct {
	out      io.Writer
	ask      *prompt.Prompter // nil = don't ask; use the saved choice
	ctl      drive.Controller
	saved    []store.Drive // drives in the database
	detected []string      // /dev/sr* present now
	stats    store.Stats
}

// run prints the summary and returns the config to use (DriveMode and
// SingleDrive possibly changed), the devices to watch, and whether the user
// answered the question (so the answer should be saved).
func (st startup) run(cfg config.Config) (_ config.Config, devices []string, asked bool, err error) {
	present := map[string]bool{}
	for _, d := range st.detected {
		present[d] = true
	}
	var usable []store.Drive
	known := map[string]bool{}
	for _, d := range st.saved {
		known[d.Device] = true
		if d.Enabled && present[d.Device] {
			usable = append(usable, d)
		}
	}

	st.printDrives(present, known)
	st.printStats()

	if len(usable) == 0 {
		return cfg, nil, false, errors.New("none of the drives set up for blurip are connected; check the drives, or run `blurip setup`")
	}
	if len(usable) == 1 {
		// Nothing to choose. The saved mode is left alone for when the
		// other drives are back.
		fmt.Fprintf(st.out, "Using %s.\n\n", usable[0].Label())
		return cfg, []string{usable[0].Device}, false, nil
	}

	if st.ask != nil {
		if cfg, err = st.askMode(cfg, usable); err != nil {
			return cfg, nil, false, err
		}
		asked = true
	}

	if cfg.DriveMode != config.DriveSingle {
		for _, d := range usable {
			devices = append(devices, d.Device)
		}
		return cfg, devices, asked, nil
	}
	chosen := usable[0]
	for _, d := range usable {
		if d.Device == cfg.SingleDrive {
			chosen = d
		}
	}
	cfg.SingleDrive = chosen.Device
	if st.ask == nil {
		fmt.Fprintf(st.out, "Using %s only (drive mode is single).\n\n", chosen.Label())
	}
	return cfg, []string{chosen.Device}, asked, nil
}

func (st startup) printDrives(present, known map[string]bool) {
	fmt.Fprintln(st.out, "Drives:")
	tw := tabwriter.NewWriter(st.out, 0, 0, 2, ' ', 0)
	for _, d := range st.saved {
		var state string
		switch {
		case !present[d.Device]:
			if !d.Enabled {
				continue // switched off and not connected: not worth mentioning
			}
			state = "not connected"
		case !d.Enabled:
			state = fmt.Sprintf("turned off (blurip config drive %s on)", store.ShortDevice(d.Device))
		default:
			state = st.status(d.Device)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", d.Label(), dash(d.Name), state)
	}
	for _, dev := range st.detected {
		if !known[dev] {
			fmt.Fprintf(tw, "  %s\t-\tnot set up (run `blurip setup` to use it)\n", shortDev(dev))
		}
	}
	tw.Flush()
	fmt.Fprintln(st.out)
}

func (st startup) status(dev string) string {
	s, err := st.ctl.Status(dev)
	if err != nil {
		return "error: " + err.Error()
	}
	return s.String()
}

func (st startup) printStats() {
	s := st.stats
	if s.Succeeded+s.Failed+s.Interrupted+s.Skipped == 0 {
		fmt.Fprintln(st.out, "No rips yet.")
		fmt.Fprintln(st.out)
		return
	}
	parts := []string{fmt.Sprintf("%d successful (%s)", s.Succeeded, humanize.IBytes(uint64(s.BytesRipped)))}
	parts = append(parts, fmt.Sprintf("%d failed", s.Failed))
	if s.Interrupted > 0 {
		parts = append(parts, fmt.Sprintf("%d interrupted", s.Interrupted))
	}
	if s.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", s.Skipped))
	}
	fmt.Fprintf(st.out, "Rips so far: %s. (blurip history for details)\n\n", strings.Join(parts, ", "))
}

func (st startup) askMode(cfg config.Config, usable []store.Drive) (config.Config, error) {
	all := fmt.Sprintf("All %d drives", len(usable))
	if len(usable) == 2 {
		all = "Both drives"
	}
	mode, err := st.ask.Choice("How should blurip use the drives this time?", []prompt.Option{
		{Value: string(config.DriveSingle), Label: "One drive only"},
		{Value: string(config.DriveSequential), Label: all + ", one rip at a time"},
		{Value: string(config.DriveParallel), Label: all + ", ripping at the same time"},
	}, string(cfg.DriveMode))
	if err != nil {
		return cfg, err
	}
	cfg.DriveMode = config.DriveMode(mode)
	if cfg.DriveMode == config.DriveSingle {
		opts := make([]prompt.Option, len(usable))
		def := usable[0].Device
		for i, d := range usable {
			opts[i] = prompt.Option{Value: d.Device, Label: d.Label()}
			if d.Device == cfg.SingleDrive {
				def = d.Device
			}
		}
		if cfg.SingleDrive, err = st.ask.Choice("Which drive?", opts, def); err != nil {
			return cfg, err
		}
	}
	fmt.Fprintln(st.out)
	return cfg, nil
}

// prepareStart gathers what the startup screen needs and runs it, saving
// the choice as the default for next time.
func prepareStart(ctx context.Context, s *store.Store, cfg config.Config, out io.Writer, ask *prompt.Prompter, ctl drive.Controller) (config.Config, []string, map[string]string, error) {
	saved, err := s.Drives(ctx)
	if err != nil {
		return cfg, nil, nil, err
	}
	detected, err := drive.Discover()
	if err != nil {
		return cfg, nil, nil, err
	}
	stats, err := s.JobStats(ctx)
	if err != nil {
		return cfg, nil, nil, err
	}
	st := startup{out: out, ask: ask, ctl: ctl, saved: saved, detected: detected, stats: stats}
	next, devices, asked, err := st.run(cfg)
	if err != nil {
		return cfg, nil, nil, err
	}
	// Remember the answer as the default for next time.
	if asked && (next.DriveMode != cfg.DriveMode || next.SingleDrive != cfg.SingleDrive) {
		if err := config.Save(ctx, s, next); err != nil {
			return cfg, nil, nil, err
		}
	}
	labels := map[string]string{}
	for _, d := range saved {
		labels[d.Device] = d.Label()
	}
	return next, devices, labels, nil
}
