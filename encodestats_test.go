package main

import (
	"strings"
	"testing"
	"time"

	"github.com/computerguymackay/blurip/internal/store"
)

func TestPrintEncodeStats(t *testing.T) {
	start := time.Date(2026, 11, 1, 20, 0, 0, 0, time.UTC)
	const gib = 1 << 30
	done := func(title, kind, preset, dest string, in, out int64, took, length time.Duration, deleted bool) store.Encode {
		return store.Encode{
			Title: title, DiscKind: kind, Preset: preset, DestName: dest, Status: store.EncodeDone,
			BytesIn: in, BytesOut: out, StartedAt: start, FinishedAt: start.Add(took), Length: length, SourceDeleted: deleted,
		}
	}
	es := []store.Encode{
		done("Ip Man", "Blu-ray", "Matroska/H.265 MKV 1080p30", "Ip Man/Ip Man", 30*gib, 6*gib, 2*time.Hour, 0, true),
		done("Wild Card", "Blu-ray", "Matroska/H.265 MKV 1080p30", "Wild Card/Wild Card", 20*gib, 4*gib, time.Hour, 90*time.Minute, true),
		done("Columbo - S01E01", "DVD", "General/Fast 480p30", "Columbo/Season 01/Columbo - S01E01", 6*gib, 2*gib, 30*time.Minute, 90*time.Minute, false),
		{Title: "Killjoys - S01E02", Status: store.EncodeWaiting},
		{Title: "Broken", Status: store.EncodeFailed},
	}
	var b strings.Builder
	printEncodeStats(&b, es)
	got := b.String()
	for _, want := range []string{
		"Queue:         1 waiting, 1 failed",
		"Encoded:       3 (2 films, 1 TV episode)",
		"Data:          56 GiB in → 12 GiB out (21% of the size), saved 44 GiB",
		"Time:          3h30m encoding, 1h10m each on average",
		"Speed:         2.0× real time", // 3h of video in 1h30m (Ip Man's length isn't known)
		"Rips deleted:  2, freeing 50 GiB",
		"Biggest saving: Ip Man, 30 GiB → 6.0 GiB",
		"Longest encode: Ip Man, 2h00m",
		"Blu-ray",
		"DVD                            1    6.0 GiB    2.0 GiB   33%    30m00s   3.0×",
		"H.265 MKV 1080p30",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestPrintEncodeStatsNothingYet(t *testing.T) {
	var b strings.Builder
	printEncodeStats(&b, nil)
	if got := b.String(); got != "Queue:         empty\nNothing has been encoded yet.\n" {
		t.Errorf("got %q", got)
	}
	// Encodes from before running times were recorded: no speed.
	b.Reset()
	start := time.Now()
	printEncodeStats(&b, []store.Encode{{Title: "Old", Status: store.EncodeDone, BytesIn: 10, BytesOut: 5,
		StartedAt: start, FinishedAt: start.Add(time.Hour)}})
	if got := b.String(); !strings.Contains(got, "Speed:         shown once") || strings.Contains(got, "ENCODES") {
		t.Errorf("got:\n%s", got)
	}
}

func TestLongDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		90 * time.Second:             "1m30s",
		5*time.Hour + 3*time.Minute:  "5h03m",
		76*time.Hour + 5*time.Minute: "3d 4h05m",
	} {
		if got := longDuration(d); got != want {
			t.Errorf("longDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
