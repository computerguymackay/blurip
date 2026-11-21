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
	b, err := s.AssignEpisodes(ctx, "Friends", 1, 1, d1.ID, []Episode{{1, 1, ""}, {2, 2, ""}, {3, 3, ""}}, t0)
	if err != nil || b.NextEpisode != 4 || b.LastDisc != 1 || b.Show != "Friends" {
		t.Fatalf("assign disc 1: %+v %v", b, err)
	}
	// Any case finds it; the saved spelling is kept.
	b, err = s.AssignEpisodes(ctx, "FRIENDS", 1, 2, d2.ID, []Episode{{1, 4, ""}, {2, 5, ""}}, t0.Add(time.Hour))
	if err != nil || b.NextEpisode != 6 || b.LastDisc != 2 || b.Show != "Friends" {
		t.Fatalf("assign disc 2: %+v %v", b, err)
	}
	// Renumbering disc 1 replaces its rows and never moves next back.
	if _, err := s.AssignEpisodes(ctx, "Friends", 1, 1, d1.ID, []Episode{{2, 1, ""}, {3, 2, ""}}, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.EpisodesForDisc(ctx, d1.ID)
	if err != nil || !ok || got.BoxSet.NextEpisode != 6 || len(got.Episodes) != 2 || got.Episodes[0] != (Episode{2, 1, ""}) || got.DiscNo != 1 {
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

func TestRenumberDiscShiftsLaterDiscs(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	d1, _ := s.UpsertDisc(ctx, Disc{Fingerprint: "f1"}, t0)
	d2, _ := s.UpsertDisc(ctx, Disc{Fingerprint: "f2"}, t0)
	b, _ := s.AssignEpisodes(ctx, "Friends", 1, 1, d1.ID, []Episode{{1, 1, ""}, {2, 2, ""}}, t0)
	s.AssignEpisodes(ctx, "Friends", 1, 2, d2.ID, []Episode{{1, 3, ""}, {2, 4, ""}}, t0)

	// Disc 1 gains an episode: disc 2 moves on by one, and so does next.
	if err := s.RenumberDisc(ctx, b.ID, d1.ID, 1, []Episode{{1, 1, ""}, {3, 2, ""}, {2, 3, ""}}, 2, 1, t0); err != nil {
		t.Fatal(err)
	}
	all, err := s.BoxSetEpisodes(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, e := range all {
		got = append(got, int(e.DiscID)*100+e.TitleID*10+e.Episode)
	}
	want := []int{int(d1.ID)*100 + 11, int(d1.ID)*100 + 32, int(d1.ID)*100 + 23, int(d2.ID)*100 + 14, int(d2.ID)*100 + 25}
	if len(got) != len(want) {
		t.Fatalf("episodes = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("episodes = %v, want %v", got, want)
		}
	}
	if b, _, _ := s.FindBoxSet(ctx, "Friends", 1); b.NextEpisode != 6 {
		t.Errorf("next = %d", b.NextEpisode)
	}
}

// A title split by chapters is several episodes: the season carries on
// after its last, and the counts include them all.
func TestSplitTitleEpisodes(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	d1, _ := s.UpsertDisc(ctx, Disc{Fingerprint: "f1", VolumeLabel: "SPACED"}, t0)
	b, err := s.AssignEpisodes(ctx, "Spaced", 1, 1, d1.ID, []Episode{{TitleID: 5, Episode: 1, Chapters: "1-5,6-10,11-15,16-20,21-25,26-30,31-35"}}, t0)
	if err != nil || b.NextEpisode != 8 {
		t.Fatalf("assign: %+v %v", b, err)
	}
	got, ok, err := s.EpisodesForDisc(ctx, d1.ID)
	if err != nil || !ok || got.Count() != 7 || !got.Split() || got.Episodes[0].Parts() != 7 {
		t.Errorf("disc = %+v %v %v", got, ok, err)
	}
	list, _ := s.BoxSets(ctx)
	discs, _ := s.BoxSetDiscs(ctx, b.ID)
	all, _ := s.BoxSetEpisodes(ctx, b.ID)
	if len(list) != 1 || list[0].Episodes != 7 || len(discs) != 1 || discs[0].Episodes != 7 || len(all) != 1 || all[0].Parts() != 7 {
		t.Errorf("box sets %+v, discs %+v, episodes %+v", list, discs, all)
	}
}

func TestDriveLeftDisc(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	s.ReplaceDrives(ctx, []Drive{{Device: "/dev/sr0"}})
	d, _ := s.UpsertDisc(ctx, Disc{Fingerprint: "f1"}, t0)
	if id, err := s.DriveLeftDisc(ctx, "/dev/sr0"); id != 0 || err != nil {
		t.Fatalf("new drive: %d %v", id, err)
	}
	s.SetDriveLeftDisc(ctx, "/dev/sr0", d.ID)
	if id, _ := s.DriveLeftDisc(ctx, "/dev/sr0"); id != d.ID {
		t.Errorf("left = %d", id)
	}
	s.SetDriveLeftDisc(ctx, "/dev/sr0", 0)
	if id, _ := s.DriveLeftDisc(ctx, "/dev/sr0"); id != 0 {
		t.Errorf("cleared = %d", id)
	}
	if id, err := s.DriveLeftDisc(ctx, "/dev/nope"); id != 0 || err != nil {
		t.Errorf("unknown drive: %d %v", id, err)
	}
}
