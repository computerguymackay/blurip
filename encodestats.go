package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/computerguymackay/blurip/internal/handbrake"
	"github.com/computerguymackay/blurip/internal/store"
)

// runEncodeStats is `blurip encode --stats`.
func runEncodeStats(ctx context.Context, dbPath string) error {
	s, _, err := openStore(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	es, err := s.Encodes(ctx)
	if err != nil {
		return err
	}
	printEncodeStats(os.Stdout, es)
	return nil
}

// encodeTotals adds up finished encodes.
type encodeTotals struct {
	n       int
	in, out int64
	took    time.Duration // encoding time, of the encodes whose times are known
	timed   int
	// Running time and encoding time of the encodes whose running time is
	// known (finished since it was recorded), for the speed.
	length, lengthTook time.Duration
}

func (t *encodeTotals) add(e store.Encode) {
	t.n++
	t.in += e.BytesIn
	t.out += e.BytesOut
	took := encodeTime(e)
	if took <= 0 {
		return
	}
	t.took += took
	t.timed++
	if e.Length > 0 {
		t.length += e.Length
		t.lengthTook += took
	}
}

// encodeTime is how long an encode took (including the length check); 0
// if not known.
func encodeTime(e store.Encode) time.Duration {
	if e.StartedAt.IsZero() || e.FinishedAt.IsZero() {
		return 0
	}
	return max(e.FinishedAt.Sub(e.StartedAt), 0)
}

// size is the encodes' total size as a share of the rips', e.g. "22%".
func (t encodeTotals) size() string {
	if t.in <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(t.out)/float64(t.in))
}

func (t encodeTotals) average() string {
	if t.timed == 0 {
		return "-"
	}
	return formatDuration(t.took / time.Duration(t.timed))
}

// speed is how many times faster than real time the encodes ran, e.g.
// "2.3×"; "-" if no running times are known.
func (t encodeTotals) speed() string {
	if t.length <= 0 || t.lengthTook <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f×", t.length.Seconds()/t.lengthTook.Seconds())
}

type encodeStats struct {
	statuses        map[string]int
	all             encodeTotals
	films, episodes int
	kinds, presets  map[string]*encodeTotals
	deleted         int
	freed           int64
	biggest         *store.Encode // biggest saving
	longest         *store.Encode // longest encode
}

func collectEncodeStats(es []store.Encode) encodeStats {
	st := encodeStats{statuses: map[string]int{}, kinds: map[string]*encodeTotals{}, presets: map[string]*encodeTotals{}}
	group := func(m map[string]*encodeTotals, key string) *encodeTotals {
		if m[key] == nil {
			m[key] = &encodeTotals{}
		}
		return m[key]
	}
	for i := range es {
		e := &es[i]
		st.statuses[e.Status]++
		if e.Status != store.EncodeDone {
			continue
		}
		st.all.add(*e)
		group(st.kinds, cmp.Or(e.DiscKind, "Other")).add(*e)
		_, preset := handbrake.SplitRef(e.Preset)
		group(st.presets, preset).add(*e)
		if showOf(*e) != "" {
			st.episodes++
		} else {
			st.films++
		}
		if e.SourceDeleted {
			st.deleted++
			st.freed += e.BytesIn
		}
		if st.biggest == nil || e.BytesIn-e.BytesOut > st.biggest.BytesIn-st.biggest.BytesOut {
			st.biggest = e
		}
		if t := encodeTime(*e); t > 0 && (st.longest == nil || t > encodeTime(*st.longest)) {
			st.longest = e
		}
	}
	return st
}

// printEncodeStats shows what the encoder has done: how many, how much
// data in and out, how long it took, split by disc type and preset.
func printEncodeStats(out io.Writer, es []store.Encode) {
	st := collectEncodeStats(es)

	var queue []string
	for _, s := range []string{store.EncodeEncoding, store.EncodeWaiting, store.EncodeFailed, store.EncodeRemoved} {
		if n := st.statuses[s]; n > 0 {
			queue = append(queue, fmt.Sprintf("%d %s", n, s))
		}
	}
	if len(queue) == 0 {
		queue = []string{"empty"}
	}
	fmt.Fprintf(out, "Queue:         %s\n", strings.Join(queue, ", "))
	if st.all.n == 0 {
		fmt.Fprintln(out, "Nothing has been encoded yet.")
		return
	}

	a := st.all
	fmt.Fprintf(out, "Encoded:       %d (%s, %s)\n", a.n, plural(st.films, "film", "films"), plural(st.episodes, "TV episode", "TV episodes"))
	saved := "saved " + bytesLabel(a.in-a.out)
	if a.out > a.in {
		saved = "grew by " + bytesLabel(a.out-a.in)
	}
	fmt.Fprintf(out, "Data:          %s in → %s out (%s of the size), %s\n", bytesLabel(a.in), bytesLabel(a.out), a.size(), saved)
	if a.timed > 0 {
		fmt.Fprintf(out, "Time:          %s encoding, %s each on average\n", longDuration(a.took), a.average())
	}
	if sp := a.speed(); sp != "-" {
		fmt.Fprintf(out, "Speed:         %s real time (running time of the film or episode ÷ time encoding)\n", sp)
	} else {
		fmt.Fprintln(out, "Speed:         shown once encodes have finished (running times are recorded from now on)")
	}
	if st.deleted > 0 {
		fmt.Fprintf(out, "Rips deleted:  %d, freeing %s\n", st.deleted, bytesLabel(st.freed))
	}
	if b := st.biggest; b != nil && b.BytesIn > b.BytesOut {
		fmt.Fprintf(out, "Biggest saving: %s, %s → %s\n", b.Title, bytesLabel(b.BytesIn), bytesLabel(b.BytesOut))
	}
	if l := st.longest; l != nil {
		fmt.Fprintf(out, "Longest encode: %s, %s\n", l.Title, formatDuration(encodeTime(*l)))
	}

	printTotalsTable(out, "DISC", st.kinds)
	printTotalsTable(out, "PRESET", st.presets)
}

// printTotalsTable prints one line per group, most encodes first; nothing
// if there is only one group (the totals above already say it all).
func printTotalsTable(out io.Writer, heading string, groups map[string]*encodeTotals) {
	if len(groups) < 2 {
		return
	}
	names := make([]string, 0, len(groups))
	for k := range groups {
		names = append(names, k)
	}
	slices.SortFunc(names, func(x, y string) int {
		return cmp.Or(cmp.Compare(groups[y].n, groups[x].n), cmp.Compare(x, y))
	})
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %-24s %7s %10s %10s %5s %9s %6s\n", heading, "ENCODES", "IN", "OUT", "SIZE", "AVG TIME", "SPEED")
	for _, k := range names {
		t := groups[k]
		fmt.Fprintf(out, "  %-24s %7d %10s %10s %5s %9s %6s\n", truncate(k, 24), t.n, bytesLabel(t.in), bytesLabel(t.out),
			t.size(), t.average(), t.speed())
	}
}

func bytesLabel(n int64) string { return humanize.IBytes(uint64(max(n, 0))) }

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// longDuration is like formatDuration, with days for long totals:
// "3d 4h05m".
func longDuration(d time.Duration) string {
	if d < 24*time.Hour {
		return formatDuration(d)
	}
	days := d / (24 * time.Hour)
	return fmt.Sprintf("%dd %s", days, formatDuration(d-days*24*time.Hour))
}
