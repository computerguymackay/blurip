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

	"github.com/computerguymackay/blurip/internal/handbrake"
	"github.com/computerguymackay/blurip/internal/store"
)

// `blurip encode history`: what has been encoded (and what failed), like
// blurip history for rips.
func runEncodeHistory(ctx context.Context, dbPath string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("encode history", flag.ContinueOnError)
	fs.SetOutput(out)
	failed := fs.Bool("failed", false, "show only failed encodes")
	limit := fs.Int("n", 50, "show at most this many encodes (0 = all)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: blurip encode history [-failed] [-n N]   list encodes, latest first")
		fmt.Fprintln(fs.Output(), "       blurip encode history ENCODE            show everything about one encode")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	s, _, err := openStore(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	if fs.NArg() > 0 {
		id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
		if err != nil || fs.NArg() > 1 {
			fs.Usage()
			return fmt.Errorf("expected one encode number, got %q", strings.Join(fs.Args(), " "))
		}
		e, err := s.GetEncode(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no encode %d (blurip encode history lists them)", id)
		} else if err != nil {
			return err
		}
		printEncode(out, e)
		return nil
	}
	es, err := s.EncodeHistory(ctx, *failed, *limit)
	if err != nil {
		return err
	}
	return printEncodeHistory(out, es)
}

// printEncodeHistory lists encodes, one line each.
func printEncodeHistory(w io.Writer, es []store.Encode) error {
	if len(es) == 0 {
		fmt.Fprintln(w, "Nothing has been encoded yet.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ENC\tFINISHED\tTOOK\tSTATUS\tTITLE\tTYPE\tPRESET\tSIZE\tSPEED\tDETAILS")
	for _, e := range es {
		finished := "-"
		if !e.FinishedAt.IsZero() {
			finished = e.FinishedAt.Local().Format("2006-01-02 15:04")
		}
		took := "-"
		if d := encodeTime(e); d > 0 {
			took = formatDuration(d)
		}
		_, preset := handbrake.SplitRef(e.Preset)
		size, details := "-", e.FailureReason
		if e.Status == store.EncodeDone {
			size = encodeSize(e)
			details = e.OutputPath
			if e.SourceDeleted {
				details += "; rip deleted"
			}
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.ID, finished, took, e.Status, e.Title,
			dash(e.DiscKind), dash(preset), size, encodeSpeed(e), dash(details))
	}
	return tw.Flush()
}

// encodeSize is "30 GiB → 1.9 GiB (6%)"; "-" if not known.
func encodeSize(e store.Encode) string {
	if e.BytesIn <= 0 || e.BytesOut <= 0 {
		return "-"
	}
	return fmt.Sprintf("%s → %s (%.0f%%)", bytesLabel(e.BytesIn), bytesLabel(e.BytesOut), 100*float64(e.BytesOut)/float64(e.BytesIn))
}

// encodeSpeed is how many times faster than real time it encoded, e.g.
// "2.3×"; "-" if not known.
func encodeSpeed(e store.Encode) string {
	t := encodeTotals{}
	if e.Status == store.EncodeDone {
		t.add(e)
	}
	return t.speed()
}

// printEncode shows everything about one encode.
func printEncode(w io.Writer, e store.Encode) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	row := func(k, v string) { fmt.Fprintf(tw, "%s\t%s\n", k, dash(v)) }
	when := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Local().Format("2006-01-02 15:04:05")
	}
	row("Encode", strconv.FormatInt(e.ID, 10))
	row("Title", e.Title+kindSuffix(e.DiscKind))
	row("Status", e.Status)
	if e.FailureReason != "" {
		row("Reason", e.FailureReason)
	}
	if e.OutputPath != "" {
		row("Saved to", e.OutputPath)
	}
	rip := e.SourcePath
	switch {
	case e.SourceDeleted:
		rip += " (deleted once encoded)"
	case e.Status == store.EncodeDone:
		if _, err := os.Stat(e.SourcePath); err != nil {
			rip += " (kept, but no longer there)"
		} else {
			rip += " (kept)"
		}
	}
	row("Rip", rip)
	if e.JobID != 0 {
		row("Ripped by", fmt.Sprintf("job %d (blurip history %d)", e.JobID, e.JobID))
	} else {
		row("Ripped by", "added to the queue by path")
	}
	if e.Status == store.EncodeDone {
		row("Size", encodeSize(e))
		if saved := e.BytesIn - e.BytesOut; saved > 0 {
			row("  saved", bytesLabel(saved))
		}
	} else if e.BytesIn > 0 {
		row("Rip size", bytesLabel(e.BytesIn))
	}
	cat, preset := handbrake.SplitRef(e.Preset)
	if cat != "" {
		preset += " (" + cat + ")"
	}
	row("Preset", preset)
	row("Tracks", tracksLabel(e))
	if e.Length > 0 {
		row("Running time", formatDuration(e.Length))
	}
	row("Queued", when(e.QueuedAt))
	row("Started", when(e.StartedAt))
	row("Finished", when(e.FinishedAt))
	if !e.StartedAt.IsZero() && !e.QueuedAt.IsZero() && e.StartedAt.After(e.QueuedAt) {
		row("  waiting in the queue", longDuration(e.StartedAt.Sub(e.QueuedAt)))
	}
	if d := encodeTime(e); d > 0 {
		row("  encoding", formatDuration(d))
	}
	if sp := encodeSpeed(e); sp != "-" {
		row("  speed", sp+" real time")
	}
	tw.Flush()
}
