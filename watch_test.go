package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/drive"
	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/store"
)

// fakeDrives is a thread-safe Controller for several drives.
type fakeDrives struct {
	mu     sync.Mutex
	st     map[string]drive.Status
	ejects map[string]int
}

func newFakeDrives(withDisc ...string) *fakeDrives {
	f := &fakeDrives{st: map[string]drive.Status{}, ejects: map[string]int{}}
	for _, d := range withDisc {
		f.st[d] = drive.DiscOK
	}
	return f
}

func (f *fakeDrives) Status(dev string) (drive.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st, ok := f.st[dev]; ok {
		return st, nil
	}
	return drive.NoDisc, nil
}

func (f *fakeDrives) Eject(dev string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ejects[dev]++
	f.st[dev] = drive.TrayOpen
	return nil
}

func (f *fakeDrives) insert(dev string) {
	f.mu.Lock()
	f.st[dev] = drive.DiscOK
	f.mu.Unlock()
}

// syncBuffer is a bytes.Buffer safe for concurrent writers.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type watchHarness struct {
	w      *watcher
	store  *store.Store
	drives *fakeDrives
	out    *syncBuffer
	cancel context.CancelFunc
	result chan error
}

func newWatchHarness(t *testing.T, mode config.DriveMode, mkvBody, answers string, devices []string, withDisc ...string) *watchHarness {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var sd []store.Drive
	for _, d := range devices {
		sd = append(sd, store.Drive{Device: d})
	}
	st.ReplaceDrives(ctx, sd)
	cfg := testConfig(t, fakeMakeMKV(t, mkvBody))
	cfg.DriveMode = mode
	out := &syncBuffer{}
	fd := newFakeDrives(withDisc...)
	return &watchHarness{
		w: &watcher{cfg: cfg, store: st, ctl: fd, devices: devices, interval: 10 * time.Millisecond,
			out: out, ask: prompt.NewPrompter(strings.NewReader(answers), out), now: time.Now},
		store: st, drives: fd, out: out,
	}
}

func (h *watchHarness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.result = make(chan error, 1)
	go func() { h.result <- h.w.run(ctx) }()
}

// waitJobs waits until n jobs have finished.
func (h *watchHarness) waitJobs(t *testing.T, n int) []store.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		jobs, _ := h.store.History(context.Background(), store.HistoryFilter{})
		finished := 0
		for _, j := range jobs {
			if j.Status != store.StatusRunning {
				finished++
			}
		}
		if finished >= n {
			return jobs
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d jobs\n%s", n, h.out)
	return nil
}

func (h *watchHarness) stop(t *testing.T) error {
	t.Helper()
	h.cancel()
	select {
	case err := <-h.result:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("watcher did not stop")
		return nil
	}
}

func statuses(jobs []store.Job) string {
	var s []string
	for _, j := range jobs {
		s = append(s, j.Status)
	}
	return strings.Join(s, ",")
}

func TestWatchSequentialTwoDrives(t *testing.T) {
	h := newWatchHarness(t, config.DriveSequential, mkvOK, "", []string{"/dev/sr0", "/dev/sr1"}, "/dev/sr0", "/dev/sr1")
	h.start()
	jobs := h.waitJobs(t, 2)
	if statuses(jobs) != "succeeded,succeeded" {
		t.Errorf("jobs = %s\n%s", statuses(jobs), h.out)
	}
	// A disc inserted later is picked up; the old ones are not re-ripped.
	h.drives.insert("/dev/sr0")
	jobs = h.waitJobs(t, 3)
	if err := h.stop(t); err != nil {
		t.Errorf("stop: %v", err)
	}
	// Same disc again in sr0: skipped as already ripped.
	if statuses(jobs) != "skipped,succeeded,succeeded" {
		t.Errorf("jobs = %s\n%s", statuses(jobs), h.out)
	}
	if !strings.Contains(h.out.String(), "Stopped.") {
		t.Errorf("output: %s", h.out)
	}
}

func TestWatchSingleModeUsesFirstDrive(t *testing.T) {
	h := newWatchHarness(t, config.DriveSingle, mkvOK, "", []string{"/dev/sr0", "/dev/sr1"}, "/dev/sr0", "/dev/sr1")
	h.start()
	h.waitJobs(t, 1)
	time.Sleep(100 * time.Millisecond)
	h.stop(t)
	jobs, _ := h.store.History(context.Background(), store.HistoryFilter{})
	if len(jobs) != 1 || jobs[0].DriveDevice != "/dev/sr0" {
		t.Errorf("jobs = %+v", jobs)
	}
	if !strings.Contains(h.out.String(), "using /dev/sr0 only") {
		t.Errorf("output: %s", h.out)
	}
}

func TestWatchKeepsGoingAfterFailure(t *testing.T) {
	// Fails the first time, succeeds after that.
	t.Setenv("BLURIP_TEST_FLAG", filepath.Join(t.TempDir(), "failed-once"))
	body := `if [ ! -e "$BLURIP_TEST_FLAG" ]; then touch "$BLURIP_TEST_FLAG"; exit 1; fi; ` + mkvOK
	h := newWatchHarness(t, config.DriveSingle, body, "", []string{"/dev/sr0"}, "/dev/sr0")
	h.start()
	h.waitJobs(t, 1)
	h.drives.insert("/dev/sr0")
	jobs := h.waitJobs(t, 2)
	h.stop(t)
	if statuses(jobs) != "succeeded,failed" {
		t.Errorf("jobs = %s\n%s", statuses(jobs), h.out)
	}
}

// barrier makes each rip wait until `n` rips have started, so the test only
// passes if they really run at the same time.
func barrier(t *testing.T, n int) string {
	dir := t.TempDir()
	t.Setenv("BLURIP_TEST_BARRIER", dir)
	return `touch "$BLURIP_TEST_BARRIER/$$"
i=0
while [ "$(ls "$BLURIP_TEST_BARRIER" | wc -l)" -lt ` + strconv.Itoa(n) + ` ] && [ $i -lt 100 ]; do sleep 0.05; i=$((i+1)); done
[ "$(ls "$BLURIP_TEST_BARRIER" | wc -l)" -ge ` + strconv.Itoa(n) + ` ] || { echo 'MSG:1,0,0,"barrier timeout","x"'; exit 3; }
echo 'PRGV:5,5,10'
printf x > "$out/x.mkv"`
}

func TestWatchParallelRipsAtTheSameTime(t *testing.T) {
	h := newWatchHarness(t, config.DriveParallel, barrier(t, 2), "", []string{"/dev/sr0", "/dev/sr1"}, "/dev/sr0", "/dev/sr1")
	h.start()
	jobs := h.waitJobs(t, 2)
	if err := h.stop(t); err != nil {
		t.Errorf("stop: %v", err)
	}
	if statuses(jobs) != "succeeded,succeeded" {
		t.Errorf("jobs = %s\n%s", statuses(jobs), h.out)
	}
	for _, want := range []string{"sr0  Reading disc...", "sr1  Reading disc..."} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, h.out)
		}
	}
	// Both rips went into the output folder without clobbering each other.
	for _, d := range []string{"Wild Card", "Wild Card (2)"} {
		if _, err := os.Stat(filepath.Join(h.w.cfg.OutputDir, d, "Wild Card.mkv")); err != nil {
			t.Error(err)
		}
	}
}

func TestWatchParallelRespectsMaxConcurrent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BLURIP_TEST_RUNNING", dir)
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("BLURIP_TEST_LOG", log)
	body := `touch "$BLURIP_TEST_RUNNING/$$"; ls "$BLURIP_TEST_RUNNING" | wc -l >> "$BLURIP_TEST_LOG"; sleep 0.2; rm "$BLURIP_TEST_RUNNING/$$"; printf x > "$out/x.mkv"`
	h := newWatchHarness(t, config.DriveParallel, body, "", []string{"/dev/sr0", "/dev/sr1", "/dev/sr2"},
		"/dev/sr0", "/dev/sr1", "/dev/sr2")
	h.w.cfg.MaxConcurrent = 1
	h.start()
	h.waitJobs(t, 3)
	h.stop(t)
	b, _ := os.ReadFile(log)
	for _, n := range strings.Fields(string(b)) {
		if n != "1" {
			t.Fatalf("saw %s rips at once with max 1: %q", n, b)
		}
	}
	if !strings.Contains(h.out.String(), "up to 1 at once") {
		t.Errorf("output: %s", h.out)
	}
}

func TestWatchParallelAskModeOneQuestionAtATime(t *testing.T) {
	// Both drives ask "what to rip" + "folder name"; answers are defaults.
	// The barrier proves the rips still run together after the questions.
	h := newWatchHarness(t, config.DriveParallel, barrier(t, 2), "\n\n\n\n", []string{"/dev/sr0", "/dev/sr1"}, "/dev/sr0", "/dev/sr1")
	h.w.cfg.RipMode = config.RipAsk
	h.start()
	jobs := h.waitJobs(t, 2)
	h.stop(t)
	if statuses(jobs) != "succeeded,succeeded" {
		t.Errorf("jobs = %s\n%s", statuses(jobs), h.out)
	}
	if n := strings.Count(h.out.String(), "Questions about the disc in"); n != 2 {
		t.Errorf("question blocks = %d\n%s", n, h.out)
	}
}

func TestWatchParallelCtrlCInterruptsAll(t *testing.T) {
	h := newWatchHarness(t, config.DriveParallel, `echo 'PRGV:1,1,10'; sleep 30`, "", []string{"/dev/sr0", "/dev/sr1"}, "/dev/sr0", "/dev/sr1")
	h.start()
	// Wait until both jobs are running.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		jobs, _ := h.store.History(context.Background(), store.HistoryFilter{})
		if len(jobs) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if err := h.stop(t); !errors.Is(err, errInterrupted) {
		t.Errorf("stop = %v", err)
	}
	jobs, _ := h.store.History(context.Background(), store.HistoryFilter{})
	if statuses(jobs) != "interrupted,interrupted" {
		t.Errorf("jobs = %s\n%s", statuses(jobs), h.out)
	}
}

func TestWatchStopWhileIdle(t *testing.T) {
	h := newWatchHarness(t, config.DriveSequential, mkvOK, "", []string{"/dev/sr0"})
	h.start()
	time.Sleep(50 * time.Millisecond)
	if err := h.stop(t); err != nil {
		t.Errorf("stop = %v", err)
	}
}

func TestBoardPanel(t *testing.T) {
	var buf bytes.Buffer
	b := newBoard(&buf, true)
	b.width = func() int { return 40 }
	b.SetHeader("blurip · watching sr0, sr1")
	r0, r1 := b.Row("/dev/sr0"), b.Row("/dev/sr1")
	r1.SetDisc("A Very Long Disc Name Indeed", "/rips/A Very Long Disc Name Indeed/x.mkv")
	r1.Progress(makemkv.Progress{Task: "Saving to MKV file", Total: 1, Max: 2})

	// The last drawn panel: header, idle sr0, sr1 with name and progress.
	screen := buf.String()
	last := screen[strings.LastIndex(screen, "\033[J")+len("\033[J"):]
	lines := strings.Split(strings.TrimSuffix(last, "\n"), "\n")
	if len(lines) != 4 || lines[1] != "sr0  waiting for a disc" ||
		!strings.HasPrefix(lines[2], "sr1  A Very Long Disc Name") || !strings.HasPrefix(lines[3], "     Saving to MKV") {
		t.Fatalf("panel = %q", lines)
	}
	for _, l := range lines {
		if n := utf8.RuneCountInString(l); n > 39 {
			t.Errorf("line wider than the terminal (%d): %q", n, l)
		}
	}
	if !strings.HasSuffix(lines[2], "…") {
		t.Errorf("long line not truncated: %q", lines[2])
	}

	// A log line goes above the panel, which is redrawn below it.
	buf.Reset()
	r0.Log("✓ Something")
	if !strings.HasPrefix(buf.String(), "\r\033[4A\033[J") || !strings.Contains(buf.String(), "sr0  ✓ Something\n") {
		t.Errorf("log output %q", buf.String())
	}

	// While paused, logs are held back and the panel is hidden.
	b.Pause()
	buf.Reset()
	r0.Log("held")
	r1.Progress(makemkv.Progress{Task: "Saving to MKV file", Total: 2, Max: 2})
	if buf.Len() != 0 {
		t.Errorf("output while paused: %q", buf.String())
	}
	b.width = func() int { return 200 }
	b.Resume()
	if !strings.Contains(buf.String(), "sr0  held") || !strings.Contains(buf.String(), "100.0%") {
		t.Errorf("after resume %q", buf.String())
	}

	r1.Idle()
	b.Close()
	if !strings.HasSuffix(buf.String(), "\033[J") {
		t.Errorf("panel not cleared on close: %q", buf.String())
	}
}

func TestBoardPlainOutput(t *testing.T) {
	var buf bytes.Buffer
	b := newBoard(&buf, false)
	r := b.Row("/dev/sr1")
	r.Activity("Reading disc...")
	r.SetDisc("Wild Card", "/rips/Wild Card/Wild Card.mkv")
	for i := 0; i <= 10; i++ {
		r.Progress(makemkv.Progress{Task: "Saving", Total: i, Max: 10})
	}
	out := buf.String()
	if strings.Contains(out, "\033") {
		t.Errorf("escape codes in plain output: %q", out)
	}
	if n := strings.Count(out, "Saving ["); n != 11 {
		t.Errorf("progress lines = %d, want one per 10%%:\n%s", n, out)
	}
	if !strings.Contains(out, "sr1  Wild Card → /rips/Wild Card/Wild Card.mkv") {
		t.Errorf("output:\n%s", out)
	}
}

func TestTruncate(t *testing.T) {
	if truncate("hello", 10) != "hello" || truncate("hello world", 6) != "hello…" || truncate("█████", 3) != "██…" {
		t.Error("truncate")
	}
}
