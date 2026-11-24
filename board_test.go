package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/computerguymackay/blurip/internal/makemkv"
)

func TestPauseShowsStillPanel(t *testing.T) {
	var out bytes.Buffer
	b := newBoard(&out, true)
	b.SetHeader("blurip · watching")
	dvd := b.Row("/dev/sr0", "DVD drive (sr0)")
	bd := b.Row("/dev/sr1", "Blu-ray drive (sr1)")
	dvd.SetDisc("The Predator", "/rips/The Predator/The Predator.mkv")
	dvd.Progress(makemkv.Progress{Task: "Saving to MKV file", Total: 50, Max: 100})
	bd.SetDisc("SBK0X7AJ", "/rips/SBK0X7AJ/SBK0X7AJ.mkv")

	out.Reset()
	b.Pause()
	got := out.String()
	for _, want := range []string{"DVD drive (sr0)      The Predator → /rips/The Predator/The Predator.mkv", "Saving to MKV file [", "50.0%", "Blu-ray drive (sr1)  SBK0X7AJ → "} {
		if !strings.Contains(got, want) {
			t.Errorf("snapshot lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "blurip · watching") {
		t.Errorf("snapshot shouldn't repeat the header:\n%s", got)
	}

	// While paused nothing redraws (the question is being typed below it).
	out.Reset()
	dvd.Progress(makemkv.Progress{Task: "Saving to MKV file", Total: 60, Max: 100})
	b.Pause()
	if out.Len() != 0 {
		t.Errorf("drew while paused: %q", out.String())
	}
	b.Resume()
	if !strings.Contains(out.String(), "60.0%") {
		t.Errorf("live panel not back after Resume:\n%s", out.String())
	}

	// Not a terminal: no panel, so no snapshot either.
	var plain bytes.Buffer
	nb := newBoard(&plain, false)
	nb.Row("/dev/sr0", "DVD drive (sr0)").SetDisc("X", "")
	plain.Reset()
	nb.Pause()
	if plain.Len() != 0 {
		t.Errorf("non-terminal printed %q", plain.String())
	}
}

// Left idle, the panel is redrawn only when something on it changes, not
// each time a loop updates it with the same text.
func TestRedrawOnlyWhenChanged(t *testing.T) {
	var out bytes.Buffer
	b := newBoard(&out, true)
	b.SetHeader("blurip encode · 0 waiting")
	row := b.Row("handbrake", "HandBrake")
	row.Activity("Waiting for rips to be queued (blurip -Q)")
	out.Reset()
	for range 100 {
		b.SetHeader("blurip encode · 0 waiting")
		row.Activity("Waiting for rips to be queued (blurip -Q)")
	}
	if out.Len() != 0 {
		t.Errorf("unchanged panel redrawn: %q", out.String())
	}
	b.SetHeader("blurip encode · 1 waiting")
	if !strings.Contains(out.String(), "1 waiting") {
		t.Errorf("changed panel not redrawn: %q", out.String())
	}
	// After a log line the panel is drawn again below it.
	out.Reset()
	b.Logf("Encoded 1, failed 0.")
	if !strings.Contains(out.String(), "1 waiting") {
		t.Errorf("panel not back after a log line: %q", out.String())
	}
}
