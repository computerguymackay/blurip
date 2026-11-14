package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/computerguymackay/blurip/internal/prompt"
)

// codeNameHarness is a harness whose disc is called "SBK0X7AJ" and whose
// answers are typed by the test (through the returned writer) whenever it
// likes, as a person would.
func codeNameHarness(t *testing.T, mkvBody string) (*harness, *syncBuffer, *io.PipeWriter) {
	t.Helper()
	h := newHarness(t, mkvBody, "")
	b, err := os.ReadFile(h.sess.cfg.MakeMKVCon)
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(string(b), `CINFO:2,0,"Wild Card"`, `CINFO:2,0,"SBK0X7AJ"`, 1)
	if err := os.WriteFile(h.sess.cfg.MakeMKVCon, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	out := &syncBuffer{}
	r, w := io.Pipe()
	t.Cleanup(func() { w.Close() })
	var mu sync.Mutex
	h.sess.out, h.sess.ask = out, prompt.NewPrompter(r, out)
	h.sess.view = newBoard(out, false).Row("/dev/sr1", "Blu-ray drive (sr1)")
	h.sess.canAsk, h.sess.pending = true, &sync.WaitGroup{}
	h.sess.askLock = func() func() { mu.Lock(); return mu.Unlock }
	return h, out, w
}

func waitFor(t *testing.T, out *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q\n%s", want, out)
}

func TestCodeNameAnsweredWhileRipping(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "gate")
	body := `while [ ! -e "` + gate + `" ]; do sleep 0.02; done; printf x > "$out/x_t01.mkv"`
	h, out, answer := codeNameHarness(t, body)

	done := make(chan error, 1)
	go func() { done <- h.sess.handleDisc(context.Background(), "/dev/sr1") }()
	waitFor(t, out, "Name for this disc [SBK0X7AJ]: ")
	io.WriteString(answer, "Wild Card: Extended\n")
	waitFor(t, out, "Wild Card - Extended →") // shown on the panel straight away
	os.WriteFile(gate, nil, 0o644)            // now the rip finishes
	if err := <-done; err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	h.sess.pending.Wait()

	want := filepath.Join(h.sess.cfg.OutputDir, "Wild Card - Extended")
	if _, err := os.Stat(filepath.Join(want, "Wild Card - Extended.mkv")); err != nil {
		t.Errorf("rip not under the new name: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(h.sess.cfg.OutputDir, "SBK0X7AJ")); err == nil {
		t.Error("code-named folder exists")
	}
	if strings.Contains(out.String(), "✎") {
		t.Errorf("named before the move, so nothing should need renaming:\n%s", out)
	}
	j := h.history(t)[0]
	if j.OutputPath != want || j.DiscName != "Wild Card - Extended" {
		t.Errorf("job = %+v", j)
	}
}

func TestCodeNameAnsweredAfterRip(t *testing.T) {
	h, out, answer := codeNameHarness(t, mkvOK)
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	old := filepath.Join(h.sess.cfg.OutputDir, "SBK0X7AJ")
	if _, err := os.Stat(filepath.Join(old, "SBK0X7AJ.mkv")); err != nil {
		t.Fatalf("rip should be in place under the code name before the answer: %v\n%s", err, out)
	}
	if h.drive.ejects != 1 {
		t.Errorf("ejects = %d: the disc should be ejected without waiting for the name", h.drive.ejects)
	}
	io.WriteString(answer, "Wild Card\n")
	h.sess.pending.Wait()

	want := filepath.Join(h.sess.cfg.OutputDir, "Wild Card")
	if _, err := os.Stat(filepath.Join(want, "Wild Card.mkv")); err != nil {
		t.Errorf("not renamed: %v\n%s", err, out)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("old folder still there")
	}
	if !strings.Contains(out.String(), "✎ SBK0X7AJ renamed to Wild Card") {
		t.Errorf("no rename message:\n%s", out)
	}
	j := h.history(t)[0]
	if j.OutputPath != want || j.DiscName != "Wild Card" {
		t.Errorf("job = %+v", j)
	}
}

func TestCodeNameKeptWhenNotAnswered(t *testing.T) {
	h, out, answer := codeNameHarness(t, mkvOK)
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	answer.Close() // input ends (or Ctrl-C) without an answer
	h.sess.pending.Wait()
	if _, err := os.Stat(filepath.Join(h.sess.cfg.OutputDir, "SBK0X7AJ", "SBK0X7AJ.mkv")); err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}

func TestGivenNameUsedNextTime(t *testing.T) {
	h, out, answer := codeNameHarness(t, `exit 1`) // the rip fails
	go io.WriteString(answer, "Wild Card\n")
	h.sess.handleDisc(context.Background(), "/dev/sr1")
	h.sess.pending.Wait()

	// The second time the disc is known by the name given; no question.
	h.sess.canAsk = false
	h.sess.handleDisc(context.Background(), "/dev/sr1")
	if !strings.Contains(out.String(), "Wild Card") || strings.Count(out.String(), "Name for this disc") != 1 {
		t.Errorf("output:\n%s", out)
	}
	for _, j := range h.history(t) {
		if j.DiscName != "Wild Card" {
			t.Errorf("job = %+v", j)
		}
	}
}

func TestNoNameQuestionForProperNames(t *testing.T) {
	h, out, _ := codeNameHarness(t, mkvOK)
	b, _ := os.ReadFile(h.sess.cfg.MakeMKVCon)
	os.WriteFile(h.sess.cfg.MakeMKVCon, []byte(strings.Replace(string(b), `"SBK0X7AJ"`, `"Wild Card"`, 1)), 0o755)
	if err := h.sess.handleDisc(context.Background(), "/dev/sr1"); err != nil {
		t.Fatal(err)
	}
	h.sess.pending.Wait()
	if strings.Contains(out.String(), "Name for this disc") {
		t.Errorf("asked anyway:\n%s", out)
	}
}
