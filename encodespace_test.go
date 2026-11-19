package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/computerguymackay/blurip/internal/store"
)

func TestEstimateEncode(t *testing.T) {
	en := store.Encode{ID: 99, Preset: "My Presets/720pFast", DiscKind: "Blu-ray"}
	done := func(preset, kind string, in, out int64) store.Encode {
		return store.Encode{Status: store.EncodeDone, Preset: preset, DiscKind: kind, BytesIn: in, BytesOut: out}
	}
	const gb = 1 << 30
	if n, basis := estimateEncode(nil, en, 30*gb); n != 15*gb || !strings.Contains(basis, "half") {
		t.Errorf("no history: %d, %s", n, basis)
	}
	hist := []store.Encode{
		done("My Presets/720pFast", "DVD", 8*gb, 4*gb),       // DVD: 50%
		done("My Presets/720pFast", "Blu-ray", 30*gb, 2*gb),  // Blu-ray: ~7%
		done("My Presets/720pFast", "Blu-ray", 40*gb, 2*gb),  // 5%
		done("General/Fast 1080p30", "Blu-ray", 30*gb, 6*gb), // another preset
		{Status: store.EncodeFailed, Preset: en.Preset, DiscKind: "Blu-ray", BytesIn: 30 * gb},
	}
	n, basis := estimateEncode(hist, en, 30*gb)
	if want := uint64(30 * gb * (2.0 / 30) * estimateMargin); n != want || !strings.Contains(basis, "last 2 720pFast Blu-ray encodes") {
		t.Errorf("same preset and kind: %d (want %d), %s", n, want, basis)
	}
	// A DVD with that preset: the DVD encode.
	if n, _ := estimateEncode(hist, store.Encode{Preset: en.Preset, DiscKind: "DVD"}, 8*gb); n != uint64(8*gb*0.5*estimateMargin) {
		t.Errorf("DVD: %d", n)
	}
	// A new preset on a Blu-ray: other Blu-ray encodes (the most: 20%).
	if n, basis := estimateEncode(hist, store.Encode{Preset: "New/One", DiscKind: "Blu-ray"}, 10*gb); n != uint64(10*gb*0.2*estimateMargin) || !strings.Contains(basis, "Blu-ray encodes") {
		t.Errorf("new preset: %d, %s", n, basis)
	}
	// Only the latest count.
	var many []store.Encode
	many = append(many, done(en.Preset, "Blu-ray", 10*gb, 9*gb)) // long ago: 90%
	for range estimateLatest {
		many = append(many, done(en.Preset, "Blu-ray", 10*gb, 1*gb))
	}
	if n, _ := estimateEncode(many, en, 10*gb); n != uint64(10*gb*0.1*estimateMargin) {
		t.Errorf("latest: %d", n)
	}
}

// No room on the destination disk: the entry stays in the queue (not
// failed) and the encoder waits.
func TestEncoderWaitsForRoom(t *testing.T) {
	old := spaceWait
	spaceWait = 20 * time.Millisecond
	t.Cleanup(func() { spaceWait = old })
	h := newEncoderHarness(t)
	out := &syncBuffer{}
	h.enc.out = out
	h.enc.cfg.MaxFillPercent = 1 // any real disk is fuller than 1%
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.enc.run(ctx) }()
	waitFor(t, out, "⏸ Wild Card: not enough space: the encode needs about")
	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "⏸") != 1 || !strings.Contains(out.String(), "guessed at half the rip") {
		t.Errorf("output:\n%s", out)
	}
	if e := h.get(t); e.Status != store.EncodeWaiting {
		t.Errorf("entry = %+v", e)
	}
	if len(h.reqs) != 0 {
		t.Error("HandBrake was run")
	}
}
