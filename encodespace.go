package main

import (
	"fmt"
	"slices"

	"github.com/computerguymackay/blurip/internal/handbrake"
	"github.com/computerguymackay/blurip/internal/store"
)

// How an encode's size is estimated from earlier encodes: the most an
// encode shrank to (encoded size / rip size) among the latest few of the
// closest kind, with some margin. Without any, half the rip's size.
const (
	estimateLatest   = 20
	estimateMargin   = 1.25
	estimateFallback = 0.5
)

// estimateEncode estimates how big the encode of a rip of srcBytes will
// be, from finished encodes (done), preferring the same preset and disc
// kind, then the same preset, the same disc kind, and then any. basis
// says what it was based on.
func estimateEncode(done []store.Encode, en store.Encode, srcBytes int64) (bytes uint64, basis string) {
	_, preset := handbrake.SplitRef(en.Preset)
	kind := en.DiscKind
	if kind == "" {
		kind = "disc"
	}
	usable := func(e store.Encode) bool {
		return e.Status == store.EncodeDone && e.BytesIn > 0 && e.BytesOut > 0 && (en.ID == 0 || e.ID != en.ID)
	}
	for _, g := range []struct {
		match func(store.Encode) bool
		what  string
	}{
		{func(e store.Encode) bool { return e.Preset == en.Preset && e.DiscKind == en.DiscKind }, fmt.Sprintf("%s %s encodes", preset, kind)},
		{func(e store.Encode) bool { return e.Preset == en.Preset }, preset + " encodes"},
		{func(e store.Encode) bool { return e.DiscKind == en.DiscKind && en.DiscKind != "" }, kind + " encodes"},
		{func(store.Encode) bool { return true }, "encodes"},
	} {
		var ratios []float64
		for _, e := range done {
			if usable(e) && g.match(e) {
				ratios = append(ratios, float64(e.BytesOut)/float64(e.BytesIn))
			}
		}
		if len(ratios) == 0 {
			continue
		}
		// done is in queue order; the latest are at the end.
		ratios = ratios[max(0, len(ratios)-estimateLatest):]
		r := min(slices.Max(ratios)*estimateMargin, 1.5)
		return uint64(float64(srcBytes) * r), fmt.Sprintf("the last %d %s shrank to at most %.0f%%", len(ratios), g.what, slices.Max(ratios)*100)
	}
	return uint64(float64(srcBytes) * estimateFallback), "no encodes yet to go by: guessed at half the rip"
}
