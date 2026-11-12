// Package drive detects discs in optical drives, ejects them and confirms
// they are gone, without going through MakeMKV (which is slow and spins the
// disc up).
package drive

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

// Status is a drive's state as reported by the CD-ROM driver.
type Status int

// Values match CDS_* in linux/cdrom.h.
const (
	NoInfo   Status = 0
	NoDisc   Status = 1
	TrayOpen Status = 2
	NotReady Status = 3 // disc inserted but still spinning up
	DiscOK   Status = 4
)

func (s Status) String() string {
	switch s {
	case NoInfo:
		return "no info"
	case NoDisc:
		return "no disc"
	case TrayOpen:
		return "tray open"
	case NotReady:
		return "not ready"
	case DiscOK:
		return "disc ready"
	}
	return fmt.Sprintf("status %d", int(s))
}

// Controller talks to drives. CDROM is the real Linux implementation.
type Controller interface {
	Status(device string) (Status, error)
	Eject(device string) error
}

// Discover returns the optical drive device paths present on this system
// (/dev/sr0, /dev/sr1, ...).
func Discover() ([]string, error) {
	devs, err := filepath.Glob("/dev/sr[0-9]*")
	sort.Strings(devs)
	return devs, err
}

// Watcher polls drives for newly inserted discs.
//
// A disc that has been dealt with (ripped, skipped, failed to eject) is
// "ignored" until it is removed, so the same disc is never picked up twice.
type Watcher struct {
	ctl      Controller
	interval time.Duration
	ignored  map[string]bool
	errs     map[string]string // last error per device, to avoid repeating it
	OnError  func(device string, err error)
}

// NewWatcher returns a watcher polling every interval.
func NewWatcher(ctl Controller, interval time.Duration) *Watcher {
	return &Watcher{ctl: ctl, interval: interval, ignored: map[string]bool{}, errs: map[string]string{}}
}

// Ignore makes the watcher skip device's current disc until it is removed.
func (w *Watcher) Ignore(device string) { w.ignored[device] = true }

// Poll checks every device once and returns those with a ready, not-ignored
// disc, in the order given.
func (w *Watcher) Poll(devices []string) []string {
	var ready []string
	for _, dev := range devices {
		st, err := w.ctl.Status(dev)
		if err != nil {
			if msg := err.Error(); w.errs[dev] != msg {
				w.errs[dev] = msg
				if w.OnError != nil {
					w.OnError(dev, err)
				}
			}
			continue
		}
		delete(w.errs, dev)
		switch st {
		case DiscOK:
			if !w.ignored[dev] {
				ready = append(ready, dev)
			}
		case NoDisc, TrayOpen:
			delete(w.ignored, dev) // disc gone: forget it
		}
	}
	return ready
}

// Wait polls until at least one device has a ready disc, and returns those
// devices. It returns ctx.Err() if ctx is cancelled first.
func (w *Watcher) Wait(ctx context.Context, devices []string) ([]string, error) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		if ready := w.Poll(devices); len(ready) > 0 {
			return ready, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

// ErrBusy is returned by Eject when the device is in use (mounted, or still
// held by MakeMKV).
var ErrBusy = errors.New("drive is busy")

// EjectOptions controls EjectAndConfirm.
type EjectOptions struct {
	Timeout    time.Duration // overall limit (default 30s)
	Interval   time.Duration // retry/poll interval (default 500ms)
	RetryAfter time.Duration // eject again if the disc is still there after this long (default 5s)
}

// EjectAndConfirm ejects the disc and waits until the drive reports that the
// disc is gone (tray open or no disc). Busy errors are retried until the
// timeout, because MakeMKV can hold the drive briefly after it exits. If an
// eject seems to work but the disc is still there after RetryAfter, it
// ejects again.
func EjectAndConfirm(ctx context.Context, ctl Controller, device string, opt EjectOptions) error {
	if opt.Timeout == 0 {
		opt.Timeout = 30 * time.Second
	}
	if opt.Interval == 0 {
		opt.Interval = 500 * time.Millisecond
	}
	if opt.RetryAfter == 0 {
		opt.RetryAfter = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()

	ejected := false
	var ejectedAt time.Time
	var lastErr error
	for {
		if ejected && time.Since(ejectedAt) >= opt.RetryAfter {
			ejected = false // the disc didn't leave: try again
		}
		if !ejected {
			switch err := ctl.Eject(device); {
			case err == nil:
				ejected, ejectedAt = true, time.Now()
			case errors.Is(err, ErrBusy):
				lastErr = err
			default:
				return fmt.Errorf("ejecting %s: %w", device, err)
			}
		}
		if ejected {
			st, err := ctl.Status(device)
			if err == nil && (st == NoDisc || st == TrayOpen) {
				return nil
			}
			lastErr = fmt.Errorf("disc still present (%s)", st)
			if err != nil {
				lastErr = err
			}
		}
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("ejecting %s: gave up after %s: %w", device, opt.Timeout, lastErr)
			}
			return ctx.Err()
		case <-time.After(opt.Interval):
		}
	}
}
