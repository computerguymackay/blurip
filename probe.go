package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/computerguymackay/blurip/internal/makemkv"
)

func runProbe(ctx context.Context, dbPath string, args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	bin := fs.String("makemkvcon", "", "makemkvcon to run: a path or command line (default: the one chosen in setup, else found as setup would)")
	file := fs.String("file", "", "parse a saved `makemkvcon -r info` output file instead of running makemkvcon")
	rawDir := fs.String("raw", "", "save raw makemkvcon output into this directory")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: blurip probe [options] [device ...]

Lists drives, then scans every drive with a disc (or only the given
devices, e.g. /dev/sr1) and prints the titles and the chosen main feature.

Options:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if *file != "" {
		f, err := os.Open(*file)
		if err != nil {
			return err
		}
		defer f.Close()
		info, err := makemkv.ParseInfo(f)
		if err != nil {
			return err
		}
		printDrives(os.Stdout, info.Drives)
		printDisc(os.Stdout, info)
		return nil
	}

	path, err := probeMakeMKV(ctx, dbPath, *bin)
	if err != nil {
		return err
	}
	if *rawDir != "" {
		if err := os.MkdirAll(*rawDir, 0o755); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	fmt.Println("Listing drives...")
	drives, err := runInfo(ctx, path, makemkv.ListDrivesSource, *rawDir, "drives.txt")
	if err != nil {
		return err
	}
	fmt.Println(drives.Version)
	printDrives(os.Stdout, drives.Drives)

	devices := fs.Args()
	if len(devices) == 0 {
		for _, d := range drives.Drives {
			if d.Exists() && d.HasDisc() {
				devices = append(devices, d.Device)
			}
		}
	}
	if len(devices) == 0 {
		fmt.Println("\nNo drive has a disc inserted.")
		return nil
	}

	for _, dev := range devices {
		fmt.Printf("\nScanning %s (this can take a minute)...\n", dev)
		start := time.Now()
		info, err := runInfo(ctx, path, makemkv.DeviceSource(dev), *rawDir, filepath.Base(dev)+"-info.txt")
		if err != nil {
			return err
		}
		fmt.Printf("Scanned in %s\n", time.Since(start).Round(time.Second))
		printDisc(os.Stdout, info)
	}
	if *rawDir != "" {
		fmt.Printf("\nRaw output saved in %s\n", *rawDir)
	}
	return nil
}

// probeMakeMKV is the makemkvcon probe runs: the one given, else the
// one saved in setup (such as MakeMKV's Flatpak), else found as setup
// finds it.
func probeMakeMKV(ctx context.Context, dbPath, given string) (string, error) {
	if given != "" {
		if err := makemkv.Check(given); err != nil {
			return "", fmt.Errorf("-makemkvcon: %w", err)
		}
		return given, nil
	}
	saved := ""
	if s, cfg, err := openStore(ctx, dbPath); err == nil {
		saved = cfg.MakeMKVCon
		s.Close()
	}
	bin, err := makemkv.Find(saved)
	if err != nil {
		return "", fmt.Errorf("makemkvcon not found: run blurip setup, or give it with -makemkvcon")
	}
	return bin, nil
}

func runInfo(ctx context.Context, bin, source, rawDir, rawName string) (*makemkv.Info, error) {
	var raw io.Writer
	if rawDir != "" {
		f, err := os.Create(filepath.Join(rawDir, rawName))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		raw = f
	}
	return makemkv.RunInfo(ctx, bin, source, raw)
}

func printDrives(w io.Writer, drives []makemkv.Drive) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DEVICE\tSTATE\tDISC\tLABEL\tDRIVE")
	for _, d := range drives {
		if !d.Exists() {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", d.Device, d.State, dash(d.DiscKind()), dash(d.DiscLabel), d.DriveName)
	}
	tw.Flush()
}

func printDisc(w io.Writer, info *makemkv.Info) {
	if len(info.Titles) == 0 {
		fmt.Fprintln(w, "\nNo titles found.")
		for _, m := range info.Messages {
			fmt.Fprintf(w, "  MSG %d: %s\n", m.Code, m.Text)
		}
		return
	}
	fmt.Fprintf(w, "\nDisc: %s  %q  (volume %s)\n", info.Disc.Type(), info.Disc.Name(), info.Disc.VolumeName())

	mf, _ := makemkv.MainFeature(info.Titles)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\tTITLE\tDURATION\tSIZE\tCHAPTERS\tSOURCE\tAUDIO\tSUBTITLES")
	for _, t := range info.Titles {
		mark := ""
		if t.ID == mf.ID {
			mark = "*"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%d\t%s\t%s\t%s\n", mark, t.ID, t.Duration(), dash(t.SizeHuman()),
			t.Chapters(), dash(t.SourceFile()), streamSummary(t.StreamsOfType("Audio"), true),
			streamSummary(t.StreamsOfType("Subtitles"), false))
	}
	tw.Flush()
	fmt.Fprintf(w, "* main feature: title %d (%s, %s)\n", mf.ID, mf.Duration(), dash(mf.SizeHuman()))
}

func streamSummary(streams []makemkv.Stream, withCodec bool) string {
	if len(streams) == 0 {
		return "-"
	}
	parts := make([]string, len(streams))
	for i, s := range streams {
		p := dash(s.LangCode())
		if withCodec && s.Codec() != "" {
			p += " " + s.Codec()
		}
		parts[i] = p
	}
	return strings.Join(parts, ", ")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
