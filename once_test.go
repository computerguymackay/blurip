package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/drive"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/rip"
	"github.com/computerguymackay/blurip/internal/store"
)

type fakeDrive struct {
	status   drive.Status
	ejects   int
	ejectErr error
}

func (f *fakeDrive) Status(string) (drive.Status, error) { return f.status, nil }
func (f *fakeDrive) Eject(string) error {
	f.ejects++
	if f.ejectErr != nil {
		return f.ejectErr
	}
	f.status = drive.TrayOpen
	return nil
}

// fakeMakeMKV answers `info` with a two-title disc and `mkv` by writing a file.
func fakeMakeMKV(t *testing.T, mkvBody string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "makemkvcon")
	script := `#!/bin/sh
for a in "$@"; do last3="$last2"; last2="$last1"; last1="$a"; done
case " $* " in
*" info "*)
echo "CINFO:32,0,\"WILD_CARD_${last1##*/}\""
cat <<'EOF2'
CINFO:1,6209,"Blu-ray disc"
CINFO:2,0,"Wild Card"
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

type harness struct {
	sess  *session
	store *store.Store
	drive *fakeDrive
	out   *bytes.Buffer
}

func newHarness(t *testing.T, mkvBody, answers string) *harness {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.ReplaceDrives(ctx, []store.Drive{{Device: "/dev/sr1", Name: "BD", Nickname: "Blu-ray drive"}})
	out := &bytes.Buffer{}
	d := &fakeDrive{status: drive.DiscOK}
	return &harness{
		sess: &session{
			cfg: testConfig(t, fakeMakeMKV(t, mkvBody)), store: st, ctl: d,
			ask: prompt.NewPrompter(strings.NewReader(answers), out), out: out,
			view: newBoard(out, false).Row("/dev/sr1", "Blu-ray drive (sr1)"), now: time.Now,
		},
		store: st, drive: d, out: out,
	}
}

func (h *harness) history(t *testing.T) []store.Job {
	t.Helper()
	jobs, err := h.store.History(context.Background(), store.HistoryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return jobs
}

const mkvOK = `echo 'PRGT:5018,0,"Saving to MKV file"'; echo 'PRGV:5,5,10'; printf "title$title" > "$out/x_t0$title.mkv"`

func TestHandleDiscSuccess(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	got, err := os.ReadFile(filepath.Join(h.sess.cfg.OutputDir, "Wild Card", "Wild Card.mkv"))
	if err != nil || string(got) != "title1" { // main feature is title 1 (largest)
		t.Errorf("output = %q, %v", got, err)
	}
	if h.drive.ejects != 1 {
		t.Errorf("ejects = %d", h.drive.ejects)
	}
	jobs := h.history(t)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %+v", jobs)
	}
	j := jobs[0]
	if j.Status != store.StatusSucceeded || j.DiscName != "Wild Card" || j.DriveDevice != "/dev/sr1" ||
		j.OutputPath != filepath.Join(h.sess.cfg.OutputDir, "Wild Card") || j.BytesWritten != 6 || j.FinishedAt.IsZero() {
		t.Errorf("job = %+v", j)
	}
}

func TestHandleDiscSkipsAlreadyRipped(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	ctx := context.Background()
	if err := h.sess.handleDisc(ctx, "/dev/sr1"); err != nil {
		t.Fatal(err)
	}
	h.drive.status = drive.DiscOK
	if err := h.sess.handleDisc(ctx, "/dev/sr1"); err != nil {
		t.Fatal(err)
	}
	jobs := h.history(t)
	if len(jobs) != 2 || jobs[0].Status != store.StatusSkipped || !strings.Contains(jobs[0].FailureReason, "already ripped") {
		t.Fatalf("jobs = %+v", jobs)
	}
	if _, err := os.Stat(filepath.Join(h.sess.cfg.OutputDir, "Wild Card (2)")); !errors.Is(err, os.ErrNotExist) {
		t.Error("skipped disc was ripped again")
	}
	if h.drive.ejects != 2 {
		t.Errorf("ejects = %d, want skipped disc ejected too", h.drive.ejects)
	}
	if !strings.Contains(h.out.String(), "already ripped on") || !strings.Contains(h.out.String(), "skipped") {
		t.Errorf("output: %s", h.out)
	}
}

func TestHandleDiscAskModeRipAgain(t *testing.T) {
	h := newHarness(t, mkvOK, "\n\n"+"y\n\n\n") // disc 1: main, default name; disc 2: rip again, main, default name
	h.sess.cfg.RipMode = config.RipAsk
	ctx := context.Background()
	h.sess.handleDisc(ctx, "/dev/sr1")
	h.drive.status = drive.DiscOK
	if err := h.sess.handleDisc(ctx, "/dev/sr1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.out.String(), "Rip it again?") {
		t.Errorf("not asked: %s", h.out)
	}
	if jobs := h.history(t); len(jobs) != 2 || jobs[0].Status != store.StatusSucceeded {
		t.Errorf("jobs = %+v", jobs)
	}
	if _, err := os.Stat(filepath.Join(h.sess.cfg.OutputDir, "Wild Card (2)", "Wild Card.mkv")); err != nil {
		t.Error(err)
	}
}

func TestHandleDiscAskModeDecline(t *testing.T) {
	h := newHarness(t, mkvOK, "\n\n"+"\n") // disc 1: main, default name; disc 2: default "no"
	h.sess.cfg.RipMode = config.RipAsk
	ctx := context.Background()
	h.sess.handleDisc(ctx, "/dev/sr1")
	h.drive.status = drive.DiscOK
	if err := h.sess.handleDisc(ctx, "/dev/sr1"); err != nil {
		t.Fatal(err)
	}
	if jobs := h.history(t); len(jobs) != 2 || jobs[0].Status != store.StatusSkipped {
		t.Errorf("jobs = %+v", jobs)
	}
}

func TestHandleDiscFailureRecordedAndEjected(t *testing.T) {
	h := newHarness(t, `echo 'MSG:5003,0,0,"Failed to save title","x"'; exit 1`, "")
	err := h.sess.handleDisc(context.Background(), "/dev/sr1")
	if !errors.Is(err, errRipFailed) {
		t.Fatalf("err = %v", err)
	}
	if h.drive.ejects != 1 {
		t.Errorf("ejects = %d", h.drive.ejects)
	}
	jobs := h.history(t)
	if len(jobs) != 1 || jobs[0].Status != store.StatusFailed || !strings.Contains(jobs[0].FailureReason, "Failed to save title") {
		t.Errorf("jobs = %+v", jobs)
	}
}

func TestHandleDiscFailedBeforeDoesNotCountAsRipped(t *testing.T) {
	h := newHarness(t, `exit 1`, "")
	ctx := context.Background()
	h.sess.handleDisc(ctx, "/dev/sr1")
	h.drive.status = drive.DiscOK
	h.sess.cfg.MakeMKVCon = fakeMakeMKV(t, mkvOK)
	if err := h.sess.handleDisc(ctx, "/dev/sr1"); err != nil {
		t.Fatal(err)
	}
	if jobs := h.history(t); len(jobs) != 2 || jobs[0].Status != store.StatusSucceeded {
		t.Errorf("jobs = %+v", jobs)
	}
}

func TestHandleDiscUnreadable(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	h.sess.cfg.MakeMKVCon = filepath.Join(t.TempDir(), "missing-makemkvcon")
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); !errors.Is(err, errRipFailed) {
		t.Fatalf("err = %v", err)
	}
	jobs := h.history(t)
	if len(jobs) != 1 || jobs[0].Status != store.StatusFailed || jobs[0].DiscID != 0 ||
		!strings.Contains(jobs[0].FailureReason, "couldn't read the disc") {
		t.Errorf("jobs = %+v", jobs)
	}
}

func TestHandleDiscNoSpace(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	h.sess.cfg.MaxFillPercent = 1 // any real disk is fuller than 1%
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); !errors.Is(err, errRipFailed) {
		t.Fatalf("err = %v", err)
	}
	if jobs := h.history(t); len(jobs) != 1 || !strings.Contains(jobs[0].FailureReason, "not enough space") {
		t.Errorf("jobs = %+v", jobs)
	}
}

func TestHandleDiscNoEject(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	h.sess.cfg.Eject = false
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil || h.drive.ejects != 0 {
		t.Errorf("err = %v, ejects = %d", err, h.drive.ejects)
	}
}

func TestHandleDiscInterrupted(t *testing.T) {
	h := newHarness(t, `echo 'PRGV:1,1,10'; sleep 30`, "")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	err := h.sess.handleDisc(ctx, "/dev/sr1")
	if !errors.Is(err, errInterrupted) || h.drive.ejects != 0 {
		t.Errorf("err = %v, ejects = %d (interrupted discs stay in the drive)", err, h.drive.ejects)
	}
	jobs := h.history(t)
	if len(jobs) != 1 || jobs[0].Status != store.StatusInterrupted || jobs[0].FinishedAt.IsZero() {
		t.Errorf("jobs = %+v", jobs)
	}
	entries, _ := os.ReadDir(h.sess.cfg.OutputDir)
	for _, e := range entries {
		if e.Name() != rip.TempDirName {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}

func TestRecoverJobs(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	ctx := context.Background()
	id, _ := h.store.StartJob(ctx, 0, 0, time.Now())
	os.MkdirAll(rip.TempDir(h.sess.cfg.OutputDir, "job-1"), 0o755)
	var out bytes.Buffer
	if err := recoverJobs(ctx, h.store, h.sess.cfg, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), fmt.Sprintf("Job %d", id)) {
		t.Errorf("output: %s", out.String())
	}
	if jobs := h.history(t); jobs[0].Status != store.StatusInterrupted {
		t.Errorf("jobs = %+v", jobs)
	}
	if left, _ := os.ReadDir(filepath.Join(h.sess.cfg.OutputDir, rip.TempDirName)); len(left) != 0 {
		t.Errorf("temp not cleaned: %v", left)
	}
}

func TestPrintHistory(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	h.sess.handleDisc(context.Background(), "/dev/sr1")
	var out bytes.Buffer
	printHistory(&out, h.history(t))
	for _, want := range []string{"JOB", "succeeded", "Wild Card", "Blu-ray drive (sr1)", "6 B"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("history missing %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	printHistory(&out, nil)
	if !strings.Contains(out.String(), "No jobs yet") {
		t.Error(out.String())
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

func TestHandleDiscAskChooseTitlesAndName(t *testing.T) {
	// Choose titles, pick 0 and 1, rename the folder.
	h := newHarness(t, mkvOK, "2\n0,1\nWild Card (2015)\n")
	h.sess.cfg.RipMode = config.RipAsk
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	dir := filepath.Join(h.sess.cfg.OutputDir, "Wild Card (2015)")
	for _, f := range []string{"x_t00.mkv", "x_t01.mkv"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Error(err)
		}
	}
	for _, want := range []string{"TITLE", "35m32s", "Choose titles", "Title 1 (2 of 2"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, h.out)
		}
	}
	if jobs := h.history(t); len(jobs) != 1 || jobs[0].Status != store.StatusSucceeded || jobs[0].OutputPath != dir {
		t.Errorf("jobs = %+v", jobs)
	}
}

func TestHandleDiscAskSkip(t *testing.T) {
	h := newHarness(t, mkvOK, "3\n")
	h.sess.cfg.RipMode = config.RipAsk
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatal(err)
	}
	jobs := h.history(t)
	if len(jobs) != 1 || jobs[0].Status != store.StatusSkipped || jobs[0].FailureReason != "skipped at your request" {
		t.Errorf("jobs = %+v", jobs)
	}
	if h.drive.ejects != 1 {
		t.Errorf("ejects = %d", h.drive.ejects)
	}
	// A skip is not a rip: next time the disc is offered again.
	h.drive.status = drive.DiscOK
	h.sess.ask = prompt.NewPrompter(strings.NewReader("\n\n"), h.out)
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatal(err)
	}
	if jobs := h.history(t); jobs[0].Status != store.StatusSucceeded {
		t.Errorf("jobs = %+v", jobs)
	}
}

func TestHandleDiscAskEOFRecordsNothing(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	h.sess.cfg.RipMode = config.RipAsk
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); !errors.Is(err, prompt.ErrAborted) {
		t.Fatalf("err = %v", err)
	}
	if jobs := h.history(t); len(jobs) != 0 {
		t.Errorf("jobs = %+v", jobs)
	}
}

func TestHandleDiscReportsNameAndFile(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatal(err)
	}
	out := h.out.String()
	target := filepath.Join(h.sess.cfg.OutputDir, "Wild Card", "Wild Card.mkv")
	for _, want := range []string{
		"Blu-ray drive (sr1)  Reading disc...",
		"Blu-ray drive (sr1)  Wild Card → " + target,
		"Blu-ray drive (sr1)  Saving to MKV file [",
		"Blu-ray drive (sr1)  ✓ Wild Card → " + target,
		"Blu-ray drive (sr1)  Ejecting...",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestHandleDiscSplitByType(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	h.sess.cfg.SplitByType = true
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	want := filepath.Join(h.sess.cfg.OutputDir, "Blu-ray", "Wild Card")
	if _, err := os.Stat(filepath.Join(want, "Wild Card.mkv")); err != nil {
		t.Errorf("rip not in the Blu-ray folder: %v", err)
	}
	if j := h.history(t); len(j) != 1 || j[0].OutputPath != want {
		t.Errorf("jobs = %+v", j)
	}
	// The temp folder stays at the top of the output folder.
	if left, _ := os.ReadDir(filepath.Join(h.sess.cfg.OutputDir, rip.TempDirName)); len(left) != 0 {
		t.Errorf("temp folder not empty: %v", left)
	}
}

func TestEjectFailureNamesTheDrive(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	h.drive.ejectErr = errors.New("tray jammed")
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatalf("%v\n%s", err, h.out)
	}
	want := "couldn't eject the disc from Blu-ray drive (sr1) — open that drive by hand: ejecting /dev/sr1: tray jammed"
	if !strings.Contains(h.out.String(), want) {
		t.Errorf("output missing %q:\n%s", want, h.out)
	}
	if !strings.Contains(h.out.String(), "Blu-ray drive (sr1)  ✓ Wild Card") {
		t.Errorf("log lines don't show the nickname:\n%s", h.out)
	}
}

func TestHandleDiscInfoTimeout(t *testing.T) {
	h := newHarness(t, mkvOK, "")
	bin := filepath.Join(t.TempDir(), "makemkvcon")
	os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755)
	h.sess.cfg.MakeMKVCon = bin
	h.sess.infoTimeout = 200 * time.Millisecond
	start := time.Now()
	err := h.sess.handleDisc(context.Background(), "/dev/sr1")
	if !errors.Is(err, errRipFailed) {
		t.Fatalf("err = %v\n%s", err, h.out)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s", d)
	}
	jobs := h.history(t)
	if len(jobs) != 1 || jobs[0].Status != store.StatusFailed || !strings.Contains(jobs[0].FailureReason, "timed out") {
		t.Errorf("jobs = %+v", jobs)
	}
	if h.drive.ejects != 1 {
		t.Errorf("ejects = %d", h.drive.ejects)
	}
}
