package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestEncodeQueue(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	a, err := s.AddEncode(ctx, Encode{SourcePath: "/r/Ip Man/Ip Man.mkv", DiscKind: "Blu-ray", Title: "Ip Man",
		Preset: "My Presets/720pFast", TracksAnswered: true, DestDir: "/m", DestName: "Ip Man/Ip Man", QueuedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.AddEncode(ctx, Encode{SourcePath: "/r/x.mkv", Title: "X", Preset: "p", DestDir: "/m", DestName: "X/X", QueuedAt: now})

	next, ok, err := s.NextEncode(ctx)
	if err != nil || !ok || next.ID != a || next.Preset != "My Presets/720pFast" || !next.TracksAnswered {
		t.Fatalf("NextEncode = %+v, %v, %v", next, ok, err)
	}
	if err := s.StartEncode(ctx, a, now); err != nil {
		t.Fatal(err)
	}
	if next, _, _ := s.NextEncode(ctx); next.ID != b {
		t.Errorf("next after starting a = %d, want %d", next.ID, b)
	}
	if n, err := s.RecoverEncodes(ctx); err != nil || n != 1 {
		t.Errorf("RecoverEncodes = %d, %v", n, err)
	}
	e, _ := s.GetEncode(ctx, a)
	if e.Status != EncodeWaiting || !e.StartedAt.IsZero() {
		t.Errorf("after recovery: %+v", e)
	}
	e.AllTracks, e.Status = true, EncodeDone
	e.OutputPath, e.BytesIn, e.BytesOut, e.SourceDeleted, e.FinishedAt = "/m/Ip Man/Ip Man.mkv", 30, 2, true, now
	e.Length = 107*time.Minute + 500*time.Millisecond
	if err := s.FinishEncode(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateEncode(ctx, e); err != nil {
		t.Fatal(err)
	}
	done, _ := s.Encodes(ctx, EncodeDone)
	if len(done) != 1 || !done[0].AllTracks || !done[0].SourceDeleted || done[0].BytesOut != 2 ||
		done[0].Length != e.Length {
		t.Errorf("done = %+v", done)
	}
	if q, _ := s.IsQueued(ctx, "/r/x.mkv"); !q {
		t.Error("x.mkv should be queued")
	}
	all, _ := s.Encodes(ctx)
	if len(all) != 2 {
		t.Errorf("all = %d entries", len(all))
	}

	if _, ok, _ := s.ShowAllTracks(ctx, "Killjoys"); ok {
		t.Error("not asked yet")
	}
	s.SetShowAllTracks(ctx, "Killjoys", true)
	if all, ok, _ := s.ShowAllTracks(ctx, "KILLJOYS"); !ok || !all {
		t.Errorf("ShowAllTracks = %v, %v", all, ok)
	}
}

func TestReorderEncodes(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var ids []int64
	for _, title := range []string{"A", "B", "C", "D", "E"} {
		id, err := s.AddEncode(ctx, Encode{SourcePath: "/r/" + title, Title: title, Preset: "p", DestDir: "/m", DestName: title, QueuedAt: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	order := func(statuses ...string) string {
		es, err := s.Encodes(ctx, statuses...)
		if err != nil {
			t.Fatal(err)
		}
		out := ""
		for _, e := range es {
			out += e.Title
		}
		return out
	}
	// B is done: reordering the open entries leaves it in its place.
	b, _ := s.GetEncode(ctx, ids[1])
	b.Status = EncodeDone
	if err := s.FinishEncode(ctx, b); err != nil {
		t.Fatal(err)
	}
	// E, A, D, C
	if err := s.ReorderEncodes(ctx, []int64{ids[4], ids[0], ids[3], ids[2]}); err != nil {
		t.Fatal(err)
	}
	if got := order(EncodeWaiting); got != "EADC" {
		t.Errorf("queue = %s, want EADC", got)
	}
	if got := order(); got != "EBADC" {
		t.Errorf("full list = %s, want EBADC (B keeps its place)", got)
	}
	if next, _, _ := s.NextEncode(ctx); next.Title != "E" {
		t.Errorf("next = %s, want E", next.Title)
	}
	// New entries go to the end.
	s.AddEncode(ctx, Encode{SourcePath: "/r/F", Title: "F", Preset: "p", DestDir: "/m", DestName: "F", QueuedAt: time.Now()})
	if got := order(EncodeWaiting); got != "EADCF" {
		t.Errorf("after adding F: %s", got)
	}
	if err := s.ReorderEncodes(ctx, []int64{999}); err == nil {
		t.Error("unknown id: no error")
	}
}
