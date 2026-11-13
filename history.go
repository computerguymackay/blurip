package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/store"
	"github.com/dustin/go-humanize"
)

func runHistory(ctx context.Context, dbPath string, args []string) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	failed := fs.Bool("failed", false, "show only failed and interrupted jobs")
	limit := fs.Int("n", 50, "show at most this many jobs (0 = all)")
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
	fmt.Fprintln(tw, "JOB\tSTARTED\tTOOK\tSTATUS\tDISC\tTYPE\tDRIVE\tDETAILS")
	for _, j := range jobs {
		took := "-"
		if d := j.Duration(); d > 0 {
			took = formatDuration(d)
		}
		details := j.FailureReason
		if j.Status == store.StatusSucceeded {
			details = fmt.Sprintf("%s (%s)", j.OutputPath, humanize.IBytes(uint64(j.BytesWritten)))
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", j.ID, j.StartedAt.Format("2006-01-02 15:04"), took,
			j.Status, orUnknown(j.DiscName), dash(makemkv.KindOf(j.DiscType)), dash(j.DriveLabel), dash(details))
	}
	return tw.Flush()
}
