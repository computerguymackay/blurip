package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 12, 20, 0, 0, 0, time.UTC)

func TestUpsertDisc(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	a, err := s.UpsertDisc(ctx, Disc{Fingerprint: "fp1", Name: "Wild Card", VolumeLabel: "WILD_CARD", Type: "Blu-ray disc"}, t0)
	if err != nil || a.ID == 0 || !a.FirstSeen.Equal(t0) {
		t.Fatalf("disc = %+v, %v", a, err)
	}
	b, err := s.UpsertDisc(ctx, Disc{Fingerprint: "fp1", Name: "Wild Card (2015)"}, t0.Add(time.Hour))
	if err != nil || b.ID != a.ID || b.Name != "Wild Card (2015)" || !b.FirstSeen.Equal(t0) {
		t.Errorf("second upsert = %+v, %v", b, err)
	}
}

func TestJobLifecycle(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	s.ReplaceDrives(ctx, []Drive{{Device: "/dev/sr1"}})
	driveID, _ := s.DriveID(ctx, "/dev/sr1")
	if missing, _ := s.DriveID(ctx, "/dev/sr9"); missing != 0 || driveID == 0 {
		t.Fatalf("DriveID: %d / %d", driveID, missing)
	}
	disc, _ := s.UpsertDisc(ctx, Disc{Fingerprint: "fp", Name: "Wild Card"}, t0)

	if _, ok, _ := s.LastSuccess(ctx, disc.ID); ok {
		t.Fatal("new disc has a success")
	}
	failed, _ := s.StartJob(ctx, disc.ID, driveID, t0)
	s.FinishJob(ctx, Job{ID: failed, FinishedAt: t0.Add(time.Minute), Status: StatusFailed, FailureReason: "read error"})
	if _, ok, _ := s.LastSuccess(ctx, disc.ID); ok {
		t.Fatal("failed job counted as success")
	}

	ok1, _ := s.StartJob(ctx, disc.ID, driveID, t0.Add(time.Hour))
	if err := s.AddJobTitle(ctx, ok1, 1, "makemkv-default", "makemkv-default"); err != nil {
		t.Fatal(err)
	}
	s.FinishJob(ctx, Job{ID: ok1, FinishedAt: t0.Add(2 * time.Hour), Status: StatusSucceeded,
		OutputPath: "/rips/Wild Card", BytesWritten: 42})

	j, ok, err := s.LastSuccess(ctx, disc.ID)
	if err != nil || !ok || j.ID != ok1 || j.OutputPath != "/rips/Wild Card" || j.Duration() != time.Hour ||
		j.DiscName != "Wild Card" || j.DriveDevice != "/dev/sr1" {
		t.Errorf("LastSuccess = %+v, %v, %v", j, ok, err)
	}

	hist, _ := s.History(ctx, HistoryFilter{})
	if len(hist) != 2 || hist[0].ID != ok1 || hist[1].FailureReason != "read error" {
		t.Errorf("History = %+v", hist)
	}
	failedOnly, _ := s.History(ctx, HistoryFilter{FailedOnly: true})
	if len(failedOnly) != 1 || failedOnly[0].ID != failed {
		t.Errorf("FailedOnly = %+v", failedOnly)
	}
	if lim, _ := s.History(ctx, HistoryFilter{Limit: 1}); len(lim) != 1 {
		t.Errorf("Limit: %d", len(lim))
	}
}

func TestRecordJobWithoutDisc(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	id, err := s.RecordJob(ctx, Job{StartedAt: t0, FinishedAt: t0, Status: StatusFailed, FailureReason: "unreadable"})
	if err != nil || id == 0 {
		t.Fatal(err)
	}
	h, _ := s.History(ctx, HistoryFilter{})
	if len(h) != 1 || h[0].DiscID != 0 || h[0].DiscName != "" {
		t.Errorf("History = %+v", h)
	}
}

func TestRecoverInterrupted(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	running, _ := s.StartJob(ctx, 0, 0, t0)
	done, _ := s.StartJob(ctx, 0, 0, t0)
	s.FinishJob(ctx, Job{ID: done, FinishedAt: t0, Status: StatusSucceeded})

	rec, err := s.RecoverInterrupted(ctx)
	if err != nil || len(rec) != 1 || rec[0].ID != running {
		t.Fatalf("RecoverInterrupted = %+v, %v", rec, err)
	}
	h, _ := s.History(ctx, HistoryFilter{FailedOnly: true})
	if len(h) != 1 || h[0].Status != StatusInterrupted || h[0].FailureReason == "" {
		t.Errorf("after recovery = %+v", h)
	}
	if rec, _ := s.RecoverInterrupted(ctx); len(rec) != 0 {
		t.Error("second recovery found jobs")
	}
}

func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.db")
	l1, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock: %v", err)
	}
	l1.Release()
	l2, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	l2.Release()
}
