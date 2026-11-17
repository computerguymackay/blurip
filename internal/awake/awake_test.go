package awake

import (
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeLock struct {
	mu            sync.Mutex
	starts, stops int
	err           error
	lost          chan error
}

func (f *fakeLock) inhibitor(onLost func(error)) *Inhibitor {
	return &Inhibitor{Why: "test", OnLost: onLost, start: func(string) (func(), <-chan error, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.starts++
		if f.err != nil {
			return nil, nil, f.err
		}
		f.lost = make(chan error, 1)
		lost := f.lost
		return func() {
			f.mu.Lock()
			f.stops++
			f.mu.Unlock()
			close(lost)
		}, lost, nil
	}}
}

func (f *fakeLock) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops
}

func TestHoldsNest(t *testing.T) {
	f := &fakeLock{}
	i := f.inhibitor(nil)
	r1 := i.Hold()
	r2 := i.Hold()
	if s, _ := f.counts(); s != 1 {
		t.Fatalf("starts = %d, want 1", s)
	}
	r1()
	r1() // twice is harmless
	if _, st := f.counts(); st != 0 {
		t.Fatal("released while still held")
	}
	r2()
	if s, st := f.counts(); s != 1 || st != 1 {
		t.Fatalf("starts, stops = %d, %d", s, st)
	}
	i.Hold()()
	if s, st := f.counts(); s != 2 || st != 2 {
		t.Fatalf("starts, stops = %d, %d", s, st)
	}
}

func TestNilDoesNothing(t *testing.T) {
	var i *Inhibitor
	i.Hold()()
}

func TestLost(t *testing.T) {
	got := make(chan error, 2)
	f := &fakeLock{err: errors.New("no logind")}
	i := f.inhibitor(func(err error) { got <- err })
	i.Hold()()
	if err := <-got; err.Error() != "no logind" {
		t.Errorf("err = %v", err)
	}

	f = &fakeLock{}
	i = f.inhibitor(func(err error) { got <- err })
	release := i.Hold()
	f.lost <- errors.New("gone")
	select {
	case err := <-got:
		if err.Error() != "gone" {
			t.Errorf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnLost not called")
	}
	release()
}

// TestSystemdInhibit takes a real lock, where systemd-logind is running.
func TestSystemdInhibit(t *testing.T) {
	if exec.Command("systemd-inhibit", "--list").Run() != nil {
		t.Skip("systemd-inhibit can't reach logind here")
	}
	var lost []error
	var mu sync.Mutex
	i := New("blurip test lock", func(err error) { mu.Lock(); lost = append(lost, err); mu.Unlock() })
	release := i.Hold()
	listed := false
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && !listed; time.Sleep(50 * time.Millisecond) {
		out, _ := exec.Command("systemd-inhibit", "--list").Output()
		listed = strings.Contains(string(out), "blurip test lock")
	}
	mu.Lock()
	l := lost
	mu.Unlock()
	if !listed {
		release()
		if len(l) > 0 {
			t.Skipf("logind refused the lock here: %v", l[0])
		}
		t.Fatal("the lock isn't listed by systemd-inhibit --list")
	}
	release()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		out, _ := exec.Command("systemd-inhibit", "--list").Output()
		if !strings.Contains(string(out), "blurip test lock") {
			mu.Lock()
			defer mu.Unlock()
			if len(lost) > 0 {
				t.Errorf("released lock reported as lost: %v", lost)
			}
			return
		}
	}
	t.Fatal("the lock is still listed after release")
}
