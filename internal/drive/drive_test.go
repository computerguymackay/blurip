package drive

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCtl is a scripted Controller.
type fakeCtl struct {
	mu       sync.Mutex
	status   map[string]Status
	statErr  map[string]error
	ejectErr []error // returned by successive Eject calls; nil after
	ejects   int
	// afterEject is the status set when Eject succeeds.
	afterEject Status
}

func (f *fakeCtl) Status(dev string) (Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.statErr[dev]; err != nil {
		return NoInfo, err
	}
	return f.status[dev], nil
}

func (f *fakeCtl) Eject(dev string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ejects++
	if len(f.ejectErr) > 0 {
		err := f.ejectErr[0]
		f.ejectErr = f.ejectErr[1:]
		if err != nil {
			return err
		}
	}
	f.status[dev] = f.afterEject
	return nil
}

func (f *fakeCtl) set(dev string, s Status) {
	f.mu.Lock()
	f.status[dev] = s
	f.mu.Unlock()
}

func newFake() *fakeCtl {
	return &fakeCtl{status: map[string]Status{}, statErr: map[string]error{}, afterEject: TrayOpen}
}

func TestPollIgnoresUntilRemoved(t *testing.T) {
	f := newFake()
	w := NewWatcher(f, time.Millisecond)
	devs := []string{"/dev/sr0", "/dev/sr1"}

	f.set("/dev/sr0", NoDisc)
	f.set("/dev/sr1", NotReady)
	if got := w.Poll(devs); len(got) != 0 {
		t.Fatalf("Poll = %v, want none (not ready yet)", got)
	}
	f.set("/dev/sr1", DiscOK)
	if got := w.Poll(devs); len(got) != 1 || got[0] != "/dev/sr1" {
		t.Fatalf("Poll = %v", got)
	}

	w.Ignore("/dev/sr1")
	if got := w.Poll(devs); len(got) != 0 {
		t.Fatalf("ignored disc returned: %v", got)
	}
	f.set("/dev/sr1", TrayOpen) // removed
	w.Poll(devs)
	f.set("/dev/sr1", DiscOK) // new disc
	if got := w.Poll(devs); len(got) != 1 {
		t.Fatalf("new disc after removal not detected: %v", got)
	}
}

func TestPollReportsErrorsOnce(t *testing.T) {
	f := newFake()
	f.statErr["/dev/sr0"] = errors.New("permission denied")
	w := NewWatcher(f, time.Millisecond)
	var n int
	w.OnError = func(string, error) { n++ }
	for i := 0; i < 3; i++ {
		w.Poll([]string{"/dev/sr0"})
	}
	if n != 1 {
		t.Errorf("OnError called %d times, want 1", n)
	}
}

func TestWait(t *testing.T) {
	f := newFake()
	f.set("/dev/sr0", NoDisc)
	w := NewWatcher(f, time.Millisecond)
	go func() {
		time.Sleep(10 * time.Millisecond)
		f.set("/dev/sr0", DiscOK)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := w.Wait(ctx, []string{"/dev/sr0"})
	if err != nil || len(got) != 1 {
		t.Errorf("Wait = %v, %v", got, err)
	}
}

func TestWaitCancelled(t *testing.T) {
	f := newFake()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := NewWatcher(f, time.Millisecond).Wait(ctx, []string{"/dev/sr0"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
}

var fast = EjectOptions{Timeout: 200 * time.Millisecond, Interval: time.Millisecond}

func TestEjectAndConfirm(t *testing.T) {
	f := newFake()
	f.set("/dev/sr0", DiscOK)
	if err := EjectAndConfirm(context.Background(), f, "/dev/sr0", fast); err != nil {
		t.Fatal(err)
	}
	if f.ejects != 1 {
		t.Errorf("ejects = %d", f.ejects)
	}
}

func TestEjectRetriesWhenBusy(t *testing.T) {
	f := newFake()
	f.set("/dev/sr0", DiscOK)
	f.ejectErr = []error{ErrBusy, ErrBusy, nil}
	if err := EjectAndConfirm(context.Background(), f, "/dev/sr0", fast); err != nil {
		t.Fatal(err)
	}
	if f.ejects != 3 {
		t.Errorf("ejects = %d, want 3", f.ejects)
	}
}

func TestEjectGivesUpWhenAlwaysBusy(t *testing.T) {
	f := newFake()
	f.set("/dev/sr0", DiscOK)
	for i := 0; i < 1000; i++ {
		f.ejectErr = append(f.ejectErr, ErrBusy)
	}
	err := EjectAndConfirm(context.Background(), f, "/dev/sr0", fast)
	if err == nil || !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "gave up") {
		t.Errorf("err = %v", err)
	}
}

func TestEjectFailsFastOnOtherErrors(t *testing.T) {
	f := newFake()
	f.ejectErr = []error{errors.New("no such device")}
	if err := EjectAndConfirm(context.Background(), f, "/dev/sr0", fast); err == nil || f.ejects != 1 {
		t.Errorf("err = %v, ejects = %d", err, f.ejects)
	}
}

func TestEjectDiscStillPresent(t *testing.T) {
	f := newFake()
	f.afterEject = DiscOK // eject "succeeds" but the disc doesn't leave
	err := EjectAndConfirm(context.Background(), f, "/dev/sr0", fast)
	if err == nil || !strings.Contains(err.Error(), "still present") {
		t.Errorf("err = %v", err)
	}
}

// stubbornCtl reports success for the first `ignore` ejects without the
// disc actually leaving.
type stubbornCtl struct {
	*fakeCtl
	ignore int
}

func (s *stubbornCtl) Eject(dev string) error {
	s.mu.Lock()
	s.ejects++
	if s.ignore > 0 {
		s.ignore--
		s.mu.Unlock()
		return nil
	}
	s.status[dev] = TrayOpen
	s.mu.Unlock()
	return nil
}

func TestEjectRetriesWhenDiscStays(t *testing.T) {
	f := newFake()
	f.set("/dev/sr0", DiscOK)
	s := &stubbornCtl{fakeCtl: f, ignore: 1}
	opt := EjectOptions{Timeout: time.Second, Interval: time.Millisecond, RetryAfter: 20 * time.Millisecond}
	if err := EjectAndConfirm(context.Background(), s, "/dev/sr0", opt); err != nil {
		t.Fatal(err)
	}
	if f.ejects != 2 {
		t.Errorf("ejects = %d, want 2", f.ejects)
	}
}

func TestOnEmptyOncePerRemoval(t *testing.T) {
	f := newFake()
	w := NewWatcher(f, time.Millisecond)
	var got []string
	w.OnEmpty = func(dev string) { got = append(got, dev) }
	devs := []string{"/dev/sr0"}
	f.set("/dev/sr0", DiscOK)
	w.Poll(devs)
	f.set("/dev/sr0", TrayOpen)
	w.Poll(devs)
	w.Poll(devs)
	f.set("/dev/sr0", NoDisc)
	w.Poll(devs) // still empty: not again
	f.set("/dev/sr0", DiscOK)
	w.Poll(devs)
	f.set("/dev/sr0", NoDisc)
	w.Poll(devs)
	if len(got) != 2 {
		t.Errorf("OnEmpty calls = %v, want 2", got)
	}
}
