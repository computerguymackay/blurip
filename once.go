package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/drive"
	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/rip"
	"github.com/dustin/go-humanize"
)

// errInterrupted makes main exit with status 130 without printing an error.
var errInterrupted = errors.New("interrupted")

// runOnce rips the main feature of the first ready disc, then exits.
func runOnce(dbPath string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	s, cfg, err := openStore(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	if !cfg.SetupComplete {
		return errors.New("blurip isn't set up yet; run `blurip setup` first")
	}
	drives, err := s.Drives(ctx)
	if err != nil {
		return err
	}
	var devices []string
	for _, d := range drives {
		if d.Enabled {
			devices = append(devices, d.Device)
		}
	}
	if len(devices) == 0 {
		return errors.New("no drives enabled; run `blurip setup`")
	}

	ctl := drive.CDROM{UseEjectCommand: true}
	w := drive.NewWatcher(ctl, time.Second)
	w.OnError = func(dev string, err error) { fmt.Fprintf(os.Stderr, "%s: %v\n", dev, err) }
	ready := w.Poll(devices)
	if len(ready) == 0 {
		return fmt.Errorf("no disc ready in %s", strings.Join(devices, ", "))
	}
	return ripDisc(ctx, cfg, ctl, ready[0])
}

func ripDisc(ctx context.Context, cfg config.Config, ctl drive.Controller, dev string) error {
	fmt.Printf("Reading disc in %s...\n", dev)
	info, err := makemkv.RunInfo(ctx, cfg.MakeMKVCon, makemkv.DeviceSource(dev), nil)
	if err != nil {
		if ctx.Err() != nil {
			return errInterrupted
		}
		return err
	}
	title, ok := makemkv.MainFeature(info.Titles)
	if !ok {
		return fmt.Errorf("MakeMKV found no titles on the disc in %s", dev)
	}
	name := rip.FolderName(info.Disc.Name(), info.Disc.VolumeName())
	fmt.Printf("%s: %s\n", info.Disc.Type(), name)
	fmt.Printf("Main feature: title %d, %s, %s, audio: %s\n",
		title.ID, title.Duration(), dash(title.SizeHuman()), streamSummary(title.StreamsOfType("Audio"), true))
	fmt.Printf("Ripping to %s\n", filepath.Join(cfg.OutputDir, name))

	start := time.Now()
	pl := newProgressLine(os.Stdout)
	res, err := rip.Run(ctx, rip.Request{
		MakeMKVCon: cfg.MakeMKVCon, Device: dev, Title: title,
		OutputDir: cfg.OutputDir, Name: name,
		JobID:          fmt.Sprintf("%s-%s", start.Format("20060102-150405"), filepath.Base(dev)),
		MaxFillPercent: cfg.MaxFillPercent,
		OnProgress:     pl.Update,
	})
	pl.Done()

	switch {
	case ctx.Err() != nil:
		fmt.Println("Interrupted; the partial rip was removed. The disc was left in the drive.")
		return errInterrupted
	case err != nil:
		fmt.Printf("Rip failed after %s: %v\n", formatDuration(time.Since(start)), err)
	default:
		fmt.Printf("Done in %s: %s (%s)\n", formatDuration(time.Since(start)),
			filepath.Join(res.Dir, strings.Join(res.Files, ", ")), humanize.IBytes(uint64(res.Bytes)))
	}

	if cfg.Eject {
		fmt.Printf("Ejecting %s... ", dev)
		if eerr := drive.EjectAndConfirm(ctx, ctl, dev, drive.EjectOptions{}); eerr != nil {
			fmt.Println("failed")
			if err == nil {
				return eerr
			}
			fmt.Fprintln(os.Stderr, "blurip:", eerr)
		} else {
			fmt.Println("done")
		}
	}
	if err != nil {
		return errors.New("rip failed")
	}
	return nil
}
