package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/store"
)

var (
	dvd = store.Drive{Device: "/dev/sr0", Name: "ASUS DRW-08D6MT", Nickname: "DVD drive", Enabled: true}
	bd  = store.Drive{Device: "/dev/sr1", Name: "TSSTcorp SH-B123L BD-ROM", Nickname: "Blu-ray drive", Enabled: true}
)

func runStartup(t *testing.T, answers string, ask bool, cfg config.Config, saved []store.Drive, detected ...string) (config.Config, []string, bool, string, error) {
	t.Helper()
	var out bytes.Buffer
	st := startup{out: &out, ctl: newFakeDrives("/dev/sr1"), saved: saved, detected: detected,
		stats: store.Stats{Succeeded: 12, Failed: 2, Skipped: 1, BytesRipped: 300 << 30}}
	if ask {
		st.ask = prompt.NewPrompter(strings.NewReader(answers), &out)
	}
	c, devices, asked, err := st.run(cfg)
	return c, devices, asked, out.String(), err
}

func TestStartupListsDrivesAndStats(t *testing.T) {
	off := store.Drive{Device: "/dev/sr2", Nickname: "Old", Enabled: false}
	gone := store.Drive{Device: "/dev/sr3", Nickname: "Loft", Enabled: true}
	cfg := config.Default()
	cfg.DriveMode = config.DriveSequential
	_, devices, _, out, err := runStartup(t, "\n", true, cfg, []store.Drive{dvd, bd, off, gone},
		"/dev/sr0", "/dev/sr1", "/dev/sr2", "/dev/sr4")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"DVD drive (sr0)", "ASUS DRW-08D6MT", "no disc",
		"Blu-ray drive (sr1)", "disc ready",
		"Old (sr2)", "turned off (blurip config drive sr2 on)",
		"Loft (sr3)", "not connected",
		"sr4", "not set up",
		"Rips so far: 12 successful (300 GiB), 2 failed, 1 skipped.",
		"1) One drive only", "2) Both drives, one rip at a time", "3) Both drives, ripping at the same time", "Choose [2]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Join(devices, ",") != "/dev/sr0,/dev/sr1" {
		t.Errorf("devices = %v", devices)
	}
}

func TestStartupChoices(t *testing.T) {
	cfg := config.Default()
	cfg.DriveMode = config.DriveSequential
	both := []store.Drive{dvd, bd}

	c, devices, asked, _, err := runStartup(t, "3\n", true, cfg, both, "/dev/sr0", "/dev/sr1")
	if err != nil || !asked || c.DriveMode != config.DriveParallel || len(devices) != 2 {
		t.Errorf("parallel: %+v %v %v %v", c, devices, asked, err)
	}

	c, devices, _, out, err := runStartup(t, "1\n2\n", true, cfg, both, "/dev/sr0", "/dev/sr1")
	if err != nil || c.DriveMode != config.DriveSingle || c.SingleDrive != "/dev/sr1" || strings.Join(devices, ",") != "/dev/sr1" {
		t.Errorf("single sr1: %+v %v %v", c, devices, err)
	}
	if !strings.Contains(out, "Which drive?") || !strings.Contains(out, "2) Blu-ray drive (sr1)") {
		t.Errorf("output:\n%s", out)
	}

	// Saved single drive is the default; not asking uses it.
	cfg.DriveMode, cfg.SingleDrive = config.DriveSingle, "/dev/sr1"
	_, devices, asked, out, _ = runStartup(t, "", false, cfg, both, "/dev/sr0", "/dev/sr1")
	if asked || strings.Join(devices, ",") != "/dev/sr1" || !strings.Contains(out, "Using Blu-ray drive (sr1) only") {
		t.Errorf("not asking: %v %v\n%s", devices, asked, out)
	}
	_, _, _, out, _ = runStartup(t, "1\n\n", true, cfg, both, "/dev/sr0", "/dev/sr1")
	if !strings.Contains(out, "Choose [1]") || !strings.Contains(out, "Which drive?") || strings.Count(out, "Choose [2]") != 1 {
		t.Errorf("defaults:\n%s", out)
	}
}

func TestStartupOneDriveConnected(t *testing.T) {
	cfg := config.Default()
	cfg.DriveMode = config.DriveParallel
	c, devices, asked, out, err := runStartup(t, "", true, cfg, []store.Drive{dvd, bd}, "/dev/sr1")
	if err != nil || asked || c.DriveMode != config.DriveParallel || strings.Join(devices, ",") != "/dev/sr1" {
		t.Errorf("%+v %v %v %v", c, devices, asked, err)
	}
	if !strings.Contains(out, "Using Blu-ray drive (sr1).") || strings.Contains(out, "How should") {
		t.Errorf("output:\n%s", out)
	}
}

func TestStartupNoDrivesConnected(t *testing.T) {
	_, _, _, out, err := runStartup(t, "", true, config.Default(), []store.Drive{dvd, bd})
	if err == nil || !strings.Contains(err.Error(), "none of the drives") {
		t.Errorf("err = %v\n%s", err, out)
	}
}

func TestStartupNoRipsYet(t *testing.T) {
	var out bytes.Buffer
	startup{out: &out}.printStats()
	if !strings.Contains(out.String(), "No rips yet.") {
		t.Errorf("output: %s", out.String())
	}
}

func TestJobStatsAndSingleDriveSetting(t *testing.T) {
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "b.db")
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.ReplaceDrives(ctx, []store.Drive{dvd, bd})
	for _, st := range []string{store.StatusSucceeded, store.StatusSucceeded, store.StatusFailed, store.StatusSkipped} {
		if _, err := s.RecordJob(ctx, store.Job{Status: st, BytesWritten: 10, StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := s.JobStats(ctx)
	if err != nil || stats.Succeeded != 2 || stats.Failed != 1 || stats.Skipped != 1 || stats.BytesRipped != 20 {
		t.Errorf("stats = %+v, %v", stats, err)
	}

	if err := run([]string{"-db", db, "config", "set", "single_drive", "blu-ray drive"}); err != nil {
		t.Fatal(err)
	}
	c, _ := config.Load(ctx, s)
	if c.SingleDrive != "/dev/sr1" {
		t.Errorf("single_drive = %q", c.SingleDrive)
	}
	if err := run([]string{"-db", db, "config", "set", "single_drive", "kitchen"}); err == nil {
		t.Error("unknown single_drive accepted")
	}
}
