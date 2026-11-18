package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/computerguymackay/blurip/internal/awake"
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
	// emptySeen counts status checks that found the drive empty since the
	// last insert: the eject's own check, then the watcher's.
	emptySeen map[string]int
}

func newFakeDrives(withDisc ...string) *fakeDrives {
	f := &fakeDrives{st: map[string]drive.Status{}, ejects: map[string]int{}, emptySeen: map[string]int{}}
	for _, d := range withDisc {
		f.st[d] = drive.DiscOK
	}
	return f
}

func (f *fakeDrives) Status(dev string) (drive.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.st[dev]
	if !ok {
		st = drive.NoDisc
	}
	if st != drive.DiscOK {
		f.emptySeen[dev]++
	}
	return st, nil
}

func (f *fakeDrives) Eject(dev string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ejects[dev]++
	f.st[dev] = drive.TrayOpen
	return nil
}

// insert puts a disc in, like a person would: only once the drive is
// empty (the last disc ejected) and the watcher has seen it so. Swapped in
// faster than blurip polls, the new disc would look like the old one.
func (f *fakeDrives) insert(dev string) {
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		f.mu.Lock()
		if f.emptySeen[dev] >= 2 {
			f.st[dev] = drive.DiscOK
			f.emptySeen[dev] = 0
			f.mu.Unlock()
			return
		}
		f.mu.Unlock()
	}
	panic("insert: the drive was never seen empty")
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

func (s *syncBuffer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.Reset()
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
	if !strings.Contains(h.out.String(), "using sr0 only") {
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
	r0, r1 := b.Row("/dev/sr0", ""), b.Row("/dev/sr1", "")
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
	r := b.Row("/dev/sr1", "")
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

// gated makes every rip wait until the returned file exists, and records
// how many rips run at once in the returned log.
func gated(t *testing.T) (body, gate, log string) {
	gate = filepath.Join(t.TempDir(), "gate")
	running := t.TempDir()
	log = filepath.Join(t.TempDir(), "log")
	t.Setenv("BLURIP_TEST_GATE", gate)
	t.Setenv("BLURIP_TEST_RUNNING", running)
	t.Setenv("BLURIP_TEST_LOG", log)
	body = `touch "$BLURIP_TEST_RUNNING/$$"; ls "$BLURIP_TEST_RUNNING" | wc -l >> "$BLURIP_TEST_LOG"
i=0; while [ ! -e "$BLURIP_TEST_GATE" ] && [ $i -lt 400 ]; do sleep 0.02; i=$((i+1)); done
rm "$BLURIP_TEST_RUNNING/$$"; echo 'PRGV:5,5,10'; printf x > "$out/x.mkv"`
	return body, gate, log
}

func (h *watchHarness) waitOutput(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(h.out.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q\n%s", want, h.out)
}

func TestWatchSequentialPreparesNextDiscDuringRip(t *testing.T) {
	body, gate, log := gated(t)
	h := newWatchHarness(t, config.DriveSequential, body, "", []string{"/dev/sr0", "/dev/sr1"}, "/dev/sr0")
	h.start()
	h.waitOutput(t, "sr0  Starting:")

	// A disc goes into sr1 while sr0 rips: it is read and queued, not ripped.
	h.drives.insert("/dev/sr1")
	h.waitOutput(t, "sr1  Ready — rips next")
	if jobs, _ := h.store.History(context.Background(), store.HistoryFilter{}); len(jobs) != 1 {
		t.Fatalf("sr1 started before sr0 finished: %s\n%s", statuses(jobs), h.out)
	}

	os.WriteFile(gate, nil, 0o644)
	jobs := h.waitJobs(t, 2)
	if err := h.stop(t); err != nil {
		t.Errorf("stop: %v", err)
	}
	if statuses(jobs) != "succeeded,succeeded" || jobs[0].DriveDevice != "/dev/sr1" {
		t.Errorf("jobs = %+v\n%s", jobs, h.out)
	}
	out := h.out.String()
	if strings.Index(out, "sr0  Ejecting...") > strings.Index(out, "sr1  Starting:") {
		t.Errorf("sr0's disc wasn't ejected before sr1 started:\n%s", out)
	}
	if b, _ := os.ReadFile(log); strings.Contains(string(b), "2") {
		t.Errorf("two rips ran at once: %q", b)
	}
	if h.drives.ejects["/dev/sr0"] != 1 || h.drives.ejects["/dev/sr1"] != 1 {
		t.Errorf("ejects = %v", h.drives.ejects)
	}
}

func TestWatchSequentialAsksAboutNextDiscDuringRip(t *testing.T) {
	body, gate, _ := gated(t)
	// Defaults for both discs: main feature, suggested folder name.
	h := newWatchHarness(t, config.DriveSequential, body, "\n\n\n\n", []string{"/dev/sr0", "/dev/sr1"}, "/dev/sr0")
	h.w.cfg.RipMode = config.RipAsk
	h.start()
	h.waitOutput(t, "sr0  Starting:")
	h.drives.insert("/dev/sr1")
	h.waitOutput(t, "Questions about the disc in sr1")
	h.waitOutput(t, "sr1  Ready — rips next")
	os.WriteFile(gate, nil, 0o644)
	jobs := h.waitJobs(t, 2)
	h.stop(t)
	if statuses(jobs) != "succeeded,succeeded" {
		t.Errorf("jobs = %s\n%s", statuses(jobs), h.out)
	}
}

func TestWatchSequentialSlowDiscDoesNotBlockOtherDrive(t *testing.T) {
	// sr0's disc can't be read in time; sr1's rips meanwhile.
	h := newWatchHarness(t, config.DriveSequential, mkvOK, "", []string{"/dev/sr0", "/dev/sr1"}, "/dev/sr0")
	real := h.w.cfg.MakeMKVCon
	slow := filepath.Join(t.TempDir(), "makemkvcon")
	os.WriteFile(slow, []byte("#!/bin/sh\ncase \"$*\" in *info*sr0*) exec sleep 30;; esac\nexec "+real+" \"$@\"\n"), 0o755)
	h.w.cfg.MakeMKVCon = slow
	h.w.infoTimeout = time.Second
	h.start()
	h.waitOutput(t, "sr0  Reading disc...")
	h.drives.insert("/dev/sr1")
	jobs := h.waitJobs(t, 2)
	h.stop(t)
	// History is newest first by start time: sr0's read began first.
	if statuses(jobs) != "succeeded,failed" || jobs[0].DriveDevice != "/dev/sr1" || !strings.Contains(jobs[1].FailureReason, "hadn't finished") {
		t.Errorf("jobs = %+v\n%s", jobs, h.out)
	}
	// sr1 finished while sr0 was still being read.
	out := h.out.String()
	if strings.Index(out, "sr1  ✓") > strings.Index(out, "sr0  ✗") {
		t.Errorf("sr1 waited for sr0:\n%s", out)
	}
}

func TestWatchSequentialCtrlCWithQueuedDisc(t *testing.T) {
	body, _, _ := gated(t)
	h := newWatchHarness(t, config.DriveSequential, body, "", []string{"/dev/sr0", "/dev/sr1"}, "/dev/sr0")
	h.start()
	h.waitOutput(t, "sr0  Starting:")
	h.drives.insert("/dev/sr1")
	h.waitOutput(t, "sr1  Ready — rips next")
	if err := h.stop(t); !errors.Is(err, errInterrupted) {
		t.Errorf("stop = %v", err)
	}
	// Only sr0 had started; the queued disc leaves no job behind.
	if jobs, _ := h.store.History(context.Background(), store.HistoryFilter{}); statuses(jobs) != "interrupted" {
		t.Errorf("jobs = %s\n%s", statuses(jobs), h.out)
	}
	if h.drives.ejects["/dev/sr1"] != 0 {
		t.Errorf("queued disc was ejected")
	}
}

func TestRipCounter(t *testing.T) {
	var c ripCounter
	a := c.start()
	b := c.start() // a and b overlap
	if got := c.finish(b); got != 2 {
		t.Errorf("b peak = %d", got)
	}
	d := c.start()
	c2 := c.start() // a, d and c2 all running
	if got := c.finish(a); got != 3 {
		t.Errorf("a peak = %d", got)
	}
	c.finish(c2)
	if got := c.finish(d); got != 3 {
		t.Errorf("d peak = %d", got)
	}
	if got := c.finish(c.start()); got != 1 {
		t.Errorf("alone peak = %d", got)
	}
}

func TestWatchRecordsModeAndTimes(t *testing.T) {
	t.Run("sequential", func(t *testing.T) {
		body, gate, _ := gated(t)
		h := newWatchHarness(t, config.DriveSequential, body, "", []string{"/dev/sr0", "/dev/sr1"}, "/dev/sr0")
		h.start()
		h.waitOutput(t, "sr0  Starting:")
		h.drives.insert("/dev/sr1")
		h.waitOutput(t, "sr1  Ready — rips next")
		time.Sleep(100 * time.Millisecond) // sr1 waits at least this long
		os.WriteFile(gate, nil, 0o644)
		jobs := h.waitJobs(t, 2)
		h.stop(t)
		for _, j := range jobs {
			if j.DriveMode != "sequential" || j.DrivesUsed != "sr0, sr1" || j.RipsAtOnce != 1 || j.RipTime <= 0 {
				t.Errorf("job = %+v", j)
			}
			if total := j.ReadTime + j.WaitTime + j.RipTime; total > j.Duration()+50*time.Millisecond {
				t.Errorf("parts %s add up to more than the total %s", total, j.Duration())
			}
		}
		if sr1 := jobs[0]; sr1.DriveDevice != "/dev/sr1" || sr1.WaitTime < 100*time.Millisecond {
			t.Errorf("sr1 wait = %s (%+v)", sr1.WaitTime, sr1)
		}
	})
	t.Run("parallel", func(t *testing.T) {
		h := newWatchHarness(t, config.DriveParallel, barrier(t, 2), "", []string{"/dev/sr0", "/dev/sr1"}, "/dev/sr0", "/dev/sr1")
		h.start()
		jobs := h.waitJobs(t, 2)
		h.stop(t)
		for _, j := range jobs {
			if j.DriveMode != "parallel" || j.RipsAtOnce != 2 {
				t.Errorf("job = %+v", j)
			}
		}
		var out bytes.Buffer
		printHistory(&out, jobs, nil)
		if !strings.Contains(out.String(), "parallel ×2") {
			t.Errorf("history:\n%s", out.String())
		}
	})
	t.Run("single", func(t *testing.T) {
		h := newWatchHarness(t, config.DriveSequential, mkvOK, "", []string{"/dev/sr0"}, "/dev/sr0")
		h.start()
		jobs := h.waitJobs(t, 1)
		h.stop(t)
		if j := jobs[0]; j.DriveMode != "single" || j.DrivesUsed != "sr0" || j.RipsAtOnce != 1 {
			t.Errorf("job = %+v", j)
		}
	})
}

func TestPrintJob(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.Local)
	j := store.Job{ID: 7, DiscName: "Wild Card", DiscType: "Blu-ray disc", Status: store.StatusSucceeded,
		OutputPath: "/rips/Blu-ray/Wild Card", BytesWritten: 1 << 30, DriveLabel: "Blu-ray drive (sr1)",
		DriveMode: "parallel", RipsAtOnce: 2, DrivesUsed: "DVD drive (sr0), Blu-ray drive (sr1)",
		StartedAt: t0, FinishedAt: t0.Add(time.Hour),
		ReadTime: 40 * time.Second, WaitTime: 0, RipTime: 59*time.Minute + 20*time.Second}
	var out bytes.Buffer
	printJob(&out, j, nil)
	for _, want := range []string{
		"Wild Card (Blu-ray)", "/rips/Blu-ray/Wild Card (1.0 GiB)", "Blu-ray drive (sr1)",
		"several drives, ripping at the same time; 2 rips ran at once during this one",
		"DVD drive (sr0), Blu-ray drive (sr1)", "Total time", "1h00m", "reading the disc", "0m40s", "59m20s",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "waiting") {
		t.Errorf("shows a zero wait:\n%s", out.String())
	}
	out.Reset()
	printJob(&out, store.Job{ID: 1, Status: store.StatusSucceeded, StartedAt: t0}, nil)
	if !strings.Contains(out.String(), "older blurip") {
		t.Errorf("old job:\n%s", out.String())
	}
}

// The PC is kept awake while discs are read and ripped, and let sleep
// again once they're done.
func TestWatchKeepsPCAwakeWhileBusy(t *testing.T) {
	h := newWatchHarness(t, config.DriveParallel, barrier(t, 2), "", []string{"/dev/sr0", "/dev/sr1"}, "/dev/sr0", "/dev/sr1")
	var mu sync.Mutex
	var events []bool
	h.w.awake = awake.NewFunc(func(held bool) { mu.Lock(); events = append(events, held); mu.Unlock() })
	h.start()
	h.waitJobs(t, 2)
	// Both discs done: the lock goes once both rows are idle again.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		e := fmt.Sprint(events)
		mu.Unlock()
		if e == "[true false]" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock events = %s, want [true false] (held once for both discs, then let go)", e)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := h.stop(t); err != nil {
		t.Errorf("stop: %v", err)
	}
}
