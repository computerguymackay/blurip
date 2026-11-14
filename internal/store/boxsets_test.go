package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBoxSetNumbering(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	d1, _ := s.UpsertDisc(ctx, Disc{Fingerprint: "f1", VolumeLabel: "FRIENDS_S1_D1"}, t0)
	d2, _ := s.UpsertDisc(ctx, Disc{Fingerprint: "f2", VolumeLabel: "FRIENDS_S1_D2"}, t0)

	if _, ok, err := s.FindBoxSet(ctx, "Friends", 1); ok || err != nil {
		t.Fatalf("found before any disc: %v %v", ok, err)
	}
	b, err := s.AssignEpisodes(ctx, "Friends", 1, 1, d1.ID, []Episode{{1, 1}, {2, 2}, {3, 3}}, t0)
	if err != nil || b.NextEpisode != 4 || b.LastDisc != 1 || b.Show != "Friends" {
		t.Fatalf("assign disc 1: %+v %v", b, err)
	}
	// Any case finds it; the saved spelling is kept.
	b, err = s.AssignEpisodes(ctx, "FRIENDS", 1, 2, d2.ID, []Episode{{1, 4}, {2, 5}}, t0.Add(time.Hour))
	if err != nil || b.NextEpisode != 6 || b.LastDisc != 2 || b.Show != "Friends" {
		t.Fatalf("assign disc 2: %+v %v", b, err)
	}
	// Renumbering disc 1 replaces its rows and never moves next back.
	if _, err := s.AssignEpisodes(ctx, "Friends", 1, 1, d1.ID, []Episode{{2, 1}, {3, 2}}, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.EpisodesForDisc(ctx, d1.ID)
	if err != nil || !ok || got.BoxSet.NextEpisode != 6 || len(got.Episodes) != 2 || got.Episodes[0] != (Episode{2, 1}) || got.DiscNo != 1 {
		t.Errorf("disc 1 = %+v %v %v", got, ok, err)
	}
	if _, ok, _ := s.EpisodesForDisc(ctx, 999); ok {
		t.Error("unknown disc has episodes")
	}

	s.AssignEpisodes(ctx, "Lost", 2, 1, 999, nil, t0.Add(3*time.Hour)) // no disc rows needed for the list
	list, err := s.BoxSets(ctx)
	if err != nil || len(list) != 2 || list[0].Show != "Lost" || list[1].Discs != 2 || list[1].Episodes != 4 {
		t.Errorf("BoxSets = %+v %v", list, err)
	}

	if err := s.SetBoxSetProgress(ctx, "friends", 1, 9, -1, t0); err != nil {
		t.Fatal(err)
	}
	if b, _, _ := s.FindBoxSet(ctx, "Friends", 1); b.NextEpisode != 9 || b.LastDisc != 2 {
		t.Errorf("after set next: %+v", b)
	}
	if err := s.SetBoxSetProgress(ctx, "Friends", 1, 0, 3, t0); err != nil {
		t.Fatal(err)
	}
	if b, _, _ := s.FindBoxSet(ctx, "Friends", 1); b.NextEpisode != 9 || b.LastDisc != 3 {
		t.Errorf("after set disc: %+v", b)
	}
	if err := s.SetBoxSetProgress(ctx, "Friends", 7, 1, -1, t0); !errors.Is(err, ErrUnknownBoxSet) {
		t.Errorf("unknown season: %v", err)
	}

	if err := s.ForgetBoxSet(ctx, "Friends", 1); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.EpisodesForDisc(ctx, d1.ID); ok {
		t.Error("episodes left after forget")
	}
	if err := s.ForgetBoxSet(ctx, "Friends", 1); !errors.Is(err, ErrUnknownBoxSet) {
		t.Errorf("forget twice: %v", err)
	}
}
