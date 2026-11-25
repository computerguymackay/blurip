package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/computerguymackay/blurip/internal/handbrake"
	"github.com/computerguymackay/blurip/internal/store"
	"golang.org/x/sys/unix"
)

func TestDirWatch(t *testing.T) {
	const ext4 = 0xEF53
	types := map[string]int64{"/rips": ext4, "/films": fuseMagic}
	var errs map[string]error
	hang := make(chan struct{})
	var calls atomic.Int32
	d := newDirWatch("/rips", "/films", "", "/films")
	d.statfs = func(dir string) (int64, error) {
		calls.Add(1)
		if dir == "/hung" {
			<-hang
		}
		return types[dir], errs[dir]
	}
	if p := d.check(); p != nil || len(d.dirs) != 2 {
		t.Fatalf("first check: %v (dirs %v)", p, d.dirs)
	}

	// The sshfs mount dropped: the folder is on the local disk now.
	types["/films"] = ext4
	if p := d.check(); len(p) != 1 || !strings.Contains(p[0], "/films looks unmounted: it was an sshfs (FUSE) mount") {
		t.Errorf("unmounted: %v", p)
	}
	// Or still mounted, but its connection gone.
	types["/films"] = fuseMagic
	errs = map[string]error{"/films": unix.ENOTCONN}
	if p := d.check(); len(p) != 1 || !strings.Contains(p[0], "lost its connection") {
		t.Errorf("not connected: %v", p)
	}

	// Hanging: reported, and not asked again while the first is stuck.
	defer func(old time.Duration) { dirCheckTimeout = old }(dirCheckTimeout)
	dirCheckTimeout = 10 * time.Millisecond
	errs = nil
	d = newDirWatch("/hung")
	d.statfs = func(dir string) (int64, error) { calls.Add(1); <-hang; return ext4, nil }
	calls.Store(0)
	for range 3 {
		if p := d.check(); len(p) != 1 || !strings.Contains(p[0], "/hung isn't answering") {
			t.Errorf("hung: %v", p)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("%d checks started for a hung folder", n)
	}
	close(hang)
	time.Sleep(20 * time.Millisecond)
	if p := d.check(); p != nil {
		t.Errorf("answered at last: %v", p)
	}
}

// encode --wait stops when a folder's mount drops while it waits.
func TestEncoderStopsWhenMountDrops(t *testing.T) {
	h := newEncoderHarness(t)
	defer func(old time.Duration) { dirCheckEvery = old }(dirCheckEvery)
	dirCheckEvery = 0
	h.enc.wait, h.enc.poll = true, time.Millisecond
	var dropped atomic.Bool
	h.enc.dirs = newDirWatch(h.enc.cfg.OutputDir, h.enc.cfg.MoviesDir, h.enc.cfg.ShowsDir)
	h.enc.dirs.statfs = func(dir string) (int64, error) {
		if dir == h.enc.cfg.MoviesDir && !dropped.Load() {
			return fuseMagic, nil
		}
		return 0xEF53, nil
	}
	encode := h.enc.encode
	h.enc.encode = func(ctx context.Context, r handbrake.Request) error {
		err := encode(ctx, r)
		dropped.Store(true) // the PC slept after this encode
		return err
	}
	done := make(chan error, 1)
	go func() { done <- h.enc.run(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, errStopEncoding) {
			t.Fatalf("err = %v\n%s", err, h.out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("still waiting")
	}
	if e := h.get(t); e.Status != store.EncodeDone {
		t.Errorf("the encode before the drop: %+v", e)
	}
	for _, want := range []string{"looks unmounted: it was an sshfs (FUSE) mount", "mount the folder again and run blurip encode"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, h.out)
		}
	}
}
