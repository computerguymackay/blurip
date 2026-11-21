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

func TestEncodeChapters(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	id, err := s.AddEncode(ctx, Encode{SourcePath: "/r/Spaced - S01E01-E07.mkv", Title: "Spaced - S01E02", Chapters: "6-10", QueuedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	e, _ := s.GetEncode(ctx, id)
	if e.Chapters != "6-10" {
		t.Errorf("chapters = %q", e.Chapters)
	}
	e.Chapters = "6-11"
	s.UpdateEncode(ctx, e)
	if e, _ := s.GetEncode(ctx, id); e.Chapters != "6-11" {
		t.Errorf("updated chapters = %q", e.Chapters)
	}
}

func TestEncodedFrom(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := time.Now()
	a, _ := s.AddEncode(ctx, Encode{SourcePath: "/r/E01.mkv", Title: "E01", Preset: "p", DestDir: "/s", DestName: "E01", QueuedAt: now})
	b, _ := s.AddEncode(ctx, Encode{SourcePath: "/r/E01.mkv", Title: "E01", Preset: "p", DestDir: "/s", DestName: "E01", QueuedAt: now})
	s.FinishEncode(ctx, Encode{ID: a, Status: EncodeDone, OutputPath: "/s/E01.mp4", FinishedAt: now})
	if ok, err := s.EncodedFrom(ctx, "/s/E01.mp4", "/r/E01.mkv", "p", b); !ok || err != nil {
		t.Errorf("same rip: %v %v", ok, err)
	}
	if ok, _ := s.EncodedFrom(ctx, "/s/E01.mp4", "/r/E02.mkv", "p", b); ok {
		t.Error("another rip's output counted")
	}
	if ok, _ := s.EncodedFrom(ctx, "/s/E01.mp4", "/r/E01.mkv", "p", a); ok {
		t.Error("its own output counted")
	}
	if ok, _ := s.EncodedFrom(ctx, "/s/E01.mp4", "/r/E01.mkv", "other", b); ok {
		t.Error("another preset's encode counted: it should be kept")
	}
	if q, _ := s.SourceStillQueued(ctx, "/r/E01.mkv", a); !q {
		t.Error("b still needs the rip")
	}
	if q, _ := s.SourceStillQueued(ctx, "/r/E01.mkv", b); q {
		t.Error("a is done: the rip isn't needed")
	}
	if es, _ := s.EncodesForSource(ctx, "/r/E01.mkv"); len(es) != 2 {
		t.Errorf("EncodesForSource = %d", len(es))
	}
	s.SetEncodeOutput(ctx, a, "/s/E02.mp4")
	if e, _ := s.GetEncode(ctx, a); e.OutputPath != "/s/E02.mp4" {
		t.Errorf("output = %q", e.OutputPath)
	}
}

func TestDiscInfo(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := time.Now()
	d, _ := s.UpsertDisc(ctx, Disc{Fingerprint: "f", Name: "Wild Card"}, now)
	if _, ok, err := s.GetDiscInfo(ctx, d.ID); ok || err != nil {
		t.Errorf("none saved: %v %v", ok, err)
	}
	for _, raw := range []string{"first read", "CINFO:2,0,\"Wild Card\"\n"} {
		if err := s.SaveDiscInfo(ctx, DiscInfo{DiscID: d.ID, MakeMKVVersion: "v1.18", ReadAt: now, Raw: []byte(raw)}); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := s.GetDiscInfo(ctx, d.ID)
	if err != nil || !ok || string(got.Raw) != "CINFO:2,0,\"Wild Card\"\n" || got.MakeMKVVersion != "v1.18" || got.ReadAt.IsZero() {
		t.Errorf("got %+v, %v, %v", got, ok, err)
	}
}
