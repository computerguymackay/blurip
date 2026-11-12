package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/drive"
)

type fakeDrive struct {
	status drive.Status
	ejects int
}

func (f *fakeDrive) Status(string) (drive.Status, error) { return f.status, nil }
func (f *fakeDrive) Eject(string) error                  { f.ejects++; f.status = drive.TrayOpen; return nil }

// fakeMakeMKV answers `info` with a two-title disc and `mkv` by writing a file.
func fakeMakeMKV(t *testing.T, mkvBody string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "makemkvcon")
	script := `#!/bin/sh
for a in "$@"; do last3="$last2"; last2="$last1"; last1="$a"; done
case " $* " in
*" info "*)
cat <<'EOF2'
CINFO:1,6209,"Blu-ray disc"
CINFO:2,0,"Wild Card"
CINFO:32,0,"WILD_CARD"
TCOUNT:2
TINFO:0,9,0,"0:35:32"
TINFO:0,11,0,"100"
TINFO:1,9,0,"1:42:39"
TINFO:1,10,0,"28.9 GB"
TINFO:1,11,0,"200"
SINFO:1,1,1,6202,"Audio"
SINFO:1,1,3,0,"eng"
SINFO:1,1,6,0,"DTS-HD MA"
EOF2
;;
*" mkv "*)
out="$last1"; title="$last2"
` + mkvBody + `
;;
esac
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func testConfig(t *testing.T, bin string) config.Config {
	c := config.Default()
	c.MakeMKVCon, c.OutputDir, c.MaxFillPercent, c.SetupComplete = bin, t.TempDir(), 100, true
	return c
}

func TestRipDiscSuccess(t *testing.T) {
	bin := fakeMakeMKV(t, `echo 'PRGT:5018,0,"Saving to MKV file"'; echo 'PRGV:5,5,10'; printf "title$title" > "$out/x_t0$title.mkv"`)
	cfg := testConfig(t, bin)
	d := &fakeDrive{status: drive.DiscOK}
	if err := ripDisc(context.Background(), cfg, d, "/dev/sr1"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(cfg.OutputDir, "Wild Card", "Wild Card.mkv"))
	if err != nil || string(got) != "title1" { // main feature is title 1 (largest)
		t.Errorf("output = %q, %v", got, err)
	}
	if d.ejects != 1 {
		t.Errorf("ejects = %d", d.ejects)
	}
}

func TestRipDiscFailureStillEjects(t *testing.T) {
	bin := fakeMakeMKV(t, `echo 'MSG:5003,0,0,"Failed to save title","x"'; exit 1`)
	cfg := testConfig(t, bin)
	d := &fakeDrive{status: drive.DiscOK}
	if err := ripDisc(context.Background(), cfg, d, "/dev/sr1"); err == nil {
		t.Fatal("expected failure")
	}
	if d.ejects != 1 {
		t.Errorf("ejects = %d", d.ejects)
	}
}

func TestRipDiscNoEject(t *testing.T) {
	bin := fakeMakeMKV(t, `printf x > "$out/a.mkv"`)
	cfg := testConfig(t, bin)
	cfg.Eject = false
	d := &fakeDrive{status: drive.DiscOK}
	if err := ripDisc(context.Background(), cfg, d, "/dev/sr1"); err != nil || d.ejects != 0 {
		t.Errorf("err = %v, ejects = %d", err, d.ejects)
	}
}

func TestRipDiscInterrupted(t *testing.T) {
	bin := fakeMakeMKV(t, `echo 'PRGV:1,1,10'; sleep 30`)
	cfg := testConfig(t, bin)
	d := &fakeDrive{status: drive.DiscOK}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	err := ripDisc(ctx, cfg, d, "/dev/sr1")
	if !errors.Is(err, errInterrupted) || d.ejects != 0 {
		t.Errorf("err = %v, ejects = %d (interrupted discs stay in the drive)", err, d.ejects)
	}
	entries, _ := os.ReadDir(cfg.OutputDir)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".blurip-tmp") {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}

func TestEstimate(t *testing.T) {
	if _, ok := estimate(2*time.Second, 0.5); ok {
		t.Error("too early to estimate")
	}
	if d, ok := estimate(10*time.Minute, 0.25); !ok || d != 30*time.Minute {
		t.Errorf("estimate = %v, %v", d, ok)
	}
	if formatDuration(90*time.Minute+5*time.Second) != "1h30m" || formatDuration(65*time.Second) != "1m05s" {
		t.Error("formatDuration")
	}
}
