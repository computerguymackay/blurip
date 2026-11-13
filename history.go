package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/store"
	"github.com/dustin/go-humanize"
)

func runHistory(ctx context.Context, dbPath string, args []string) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	failed := fs.Bool("failed", false, "show only failed and interrupted jobs")
	limit := fs.Int("n", 50, "show at most this many jobs (0 = all)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: blurip history [-failed] [-n N]   list jobs, newest first")
		fmt.Fprintln(fs.Output(), "       blurip history JOB                show everything about one job")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	if fs.NArg() > 0 {
		id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
		if err != nil || fs.NArg() > 1 {
			fs.Usage()
			return fmt.Errorf("expected one job number, got %q", strings.Join(fs.Args(), " "))
		}
		j, err := s.Job(ctx, id)
		if err != nil {
			return err
		}
		printJob(os.Stdout, j)
		return nil
	}
	jobs, err := s.History(ctx, store.HistoryFilter{FailedOnly: *failed, Limit: *limit})
	if err != nil {
		return err
	}
	return printHistory(os.Stdout, jobs)
}

func printHistory(w io.Writer, jobs []store.Job) error {
	if len(jobs) == 0 {
		fmt.Fprintln(w, "No jobs yet.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "JOB\tSTARTED\tTOOK\tSTATUS\tDISC\tTYPE\tDRIVE\tMODE\tDETAILS")
	for _, j := range jobs {
		took := "-"
		if d := j.Duration(); d > 0 {
			took = formatDuration(d)
		}
		details := j.FailureReason
		if j.Status == store.StatusSucceeded {
			details = fmt.Sprintf("%s (%s)", j.OutputPath, humanize.IBytes(uint64(j.BytesWritten)))
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", j.ID, j.StartedAt.Format("2006-01-02 15:04"), took,
			j.Status, orUnknown(j.DiscName), dash(makemkv.KindOf(j.DiscType)), dash(j.DriveLabel), dash(shortMode(j)), dash(details))
	}
	return tw.Flush()
}

// shortMode is the MODE column: the drive mode, plus how many rips ran at
// once when more than one did ("parallel ×2").
func shortMode(j store.Job) string {
	m := j.DriveMode
	if j.RipsAtOnce > 1 {
		m += fmt.Sprintf(" ×%d", j.RipsAtOnce)
	}
	return m
}

// describeMode says in words how the disc was ripped.
func describeMode(j store.Job) string {
	var m string
	switch config.DriveMode(j.DriveMode) {
	case "":
		return "not recorded (ripped by an older blurip)"
	case config.DriveSingle:
		m = "single drive"
	case config.DriveSequential:
		m = "several drives, one rip at a time"
	case config.DriveParallel:
		m = "several drives, ripping at the same time"
	default:
		m = j.DriveMode
	}
	switch {
	case j.RipsAtOnce > 1:
		m += fmt.Sprintf("; %d rips ran at once during this one", j.RipsAtOnce)
	case j.RipsAtOnce == 1 && j.DriveMode == string(config.DriveParallel):
		m += "; no other rip ran during this one"
	}
	return m
}

func printJob(w io.Writer, j store.Job) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	row := func(k, v string) { fmt.Fprintf(tw, "%s\t%s\n", k, dash(v)) }
	dur := func(d time.Duration) string {
		if d <= 0 {
			return ""
		}
		return formatDuration(d)
	}
	row("Job", strconv.FormatInt(j.ID, 10))
	row("Disc", orUnknown(j.DiscName)+kindSuffix(j.DiscType))
	row("Status", j.Status)
	if j.FailureReason != "" {
		row("Reason", j.FailureReason)
	}
	if j.Status == store.StatusSucceeded {
		row("Saved to", fmt.Sprintf("%s (%s)", j.OutputPath, humanize.IBytes(uint64(j.BytesWritten))))
	}
	row("Drive", j.DriveLabel)
	row("Mode", describeMode(j))
	row("Drives in use", j.DrivesUsed)
	row("Started", j.StartedAt.Local().Format("2006-01-02 15:04:05"))
	if !j.FinishedAt.IsZero() {
		row("Finished", j.FinishedAt.Local().Format("2006-01-02 15:04:05"))
	}
	row("Total time", dur(j.Duration()))
	if j.DriveMode != "" {
		row("  reading the disc", dur(j.ReadTime))
		if j.WaitTime >= time.Second {
			row("  waiting for its turn", dur(j.WaitTime))
		}
		row("  ripping", dur(j.RipTime))
	}
	tw.Flush()
}

func kindSuffix(discType string) string {
	if k := makemkv.KindOf(discType); k != "" {
		return " (" + k + ")"
	}
	return ""
}
