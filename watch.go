package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/drive"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/space"
	"github.com/computerguymackay/blurip/internal/store"
)

// watcher runs the main loop: wait for discs and handle them according to
// the drive mode, until ctx is cancelled.
type watcher struct {
	cfg      config.Config
	store    *store.Store
	ctl      drive.Controller
	devices  []string
	interval time.Duration
	out      io.Writer // the board draws here; questions are asked here
	ask      *prompt.Prompter
	tty      bool // out is a terminal: draw the live panel
	now      func() time.Time
}

// run returns nil when stopped while idle, errInterrupted if a rip was
// interrupted, or the first fatal error (database, closed input).
func (w *watcher) run(ctx context.Context) error {
	b := newBoard(w.out, w.tty)
	defer b.Close()

	devices := w.devices
	if w.cfg.DriveMode == config.DriveSingle && len(devices) > 1 {
		b.Logf("Drive mode is single but %d drives are enabled; using %s only.", len(devices), devices[0])
		devices = devices[:1]
	}
	rows := map[string]*driveRow{}
	for _, d := range devices {
		rows[d] = b.Row(d)
	}
	names := make([]string, len(devices))
	for i, d := range devices {
		names[i] = shortDev(d)
	}

	if w.cfg.DriveMode == config.DriveParallel && len(devices) > 1 {
		mode := "parallel"
		if l := w.cfg.MaxConcurrent; l > 0 && l < len(devices) {
			mode = fmt.Sprintf("parallel, up to %d at once", l)
		}
		b.SetHeader(fmt.Sprintf("blurip · watching %s (%s) · Ctrl-C to stop", strings.Join(names, ", "), mode))
		if !w.tty {
			b.Logf("Watching %s (%s). Press Ctrl-C to stop.", strings.Join(names, ", "), mode)
		}
		return w.runParallel(ctx, b, devices, rows)
	}
	b.SetHeader(fmt.Sprintf("blurip · watching %s · Ctrl-C to stop", strings.Join(names, ", ")))
	if !w.tty {
		b.Logf("Watching %s. Press Ctrl-C to stop.", strings.Join(names, ", "))
	}
	return w.runSequential(ctx, b, devices, rows)
}

func (w *watcher) newDriveWatcher(b *board) *drive.Watcher {
	dw := drive.NewWatcher(w.ctl, w.interval)
	dw.OnError = func(dev string, err error) { b.Logf("⚠ %s: %v", shortDev(dev), err) }
	return dw
}

func (w *watcher) session(row *driveRow) *session {
	return &session{cfg: w.cfg, store: w.store, ctl: w.ctl, ask: w.ask, out: w.out, view: row, now: w.now}
}

// runSequential handles one disc at a time. No drive is watched while a
// disc is being handled; afterwards all drives are checked again.
func (w *watcher) runSequential(ctx context.Context, b *board, devices []string, rows map[string]*driveRow) error {
	dw := w.newDriveWatcher(b)
	for {
		ready, err := dw.Wait(ctx, devices)
		if err != nil {
			b.Logf("Stopped.")
			return nil // cancelled while idle
		}
		dev := ready[0]
		sess := w.session(rows[dev])
		sess.askLock = func() func() { b.Pause(); return b.Resume }
		err = sess.handleDisc(ctx, dev)
		dw.Ignore(dev) // whatever happened, don't pick this disc up again until it's removed
		switch {
		case err == nil, errors.Is(err, errRipFailed):
			// recorded and reported; keep watching
		default:
			return err
		}
	}
}

type jobDone struct {
	dev string
	err error
}

// runParallel handles discs in several drives at once (up to
// MaxConcurrent, 0 = one per drive). A drive is not watched while its disc
// is being handled; idle drives keep being watched.
func (w *watcher) runParallel(ctx context.Context, b *board, devices []string, rows map[string]*driveRow) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	limit := w.cfg.MaxConcurrent
	reservations := &space.Reservations{}
	var askMu sync.Mutex

	dw := w.newDriveWatcher(b)
	busy := map[string]bool{}
	done := make(chan jobDone)
	active := 0
	var fatal error
	interrupted := false
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	ctxDone := ctx.Done()

	for {
		if ctx.Err() == nil && (limit == 0 || active < limit) {
			var idle []string
			for _, d := range devices {
				if !busy[d] {
					idle = append(idle, d)
				}
			}
			for _, dev := range dw.Poll(idle) {
				if limit > 0 && active >= limit {
					break
				}
				busy[dev] = true
				active++
				sess := w.session(rows[dev])
				sess.reservations = reservations
				sess.askLock = func() func() {
					askMu.Lock()
					b.Pause()
					fmt.Fprintf(w.out, "\nQuestions about the disc in %s:\n", dev)
					return func() { b.Resume(); askMu.Unlock() }
				}
				go func(dev string) {
					done <- jobDone{dev, sess.handleDisc(ctx, dev)}
				}(dev)
			}
		}
		if ctx.Err() != nil && active == 0 {
			break
		}
		select {
		case d := <-done:
			active--
			busy[d.dev] = false
			dw.Ignore(d.dev)
			switch {
			case d.err == nil, errors.Is(d.err, errRipFailed):
			case errors.Is(d.err, errInterrupted):
				interrupted = true
			default:
				if fatal == nil {
					fatal = fmt.Errorf("%s: %w", d.dev, d.err)
					cancel() // stop the other rips too
				}
			}
		case <-ticker.C:
		case <-ctxDone:
			ctxDone = nil
			if active > 0 {
				b.Logf("Stopping %d rip(s)...", active)
			}
		}
	}
	switch {
	case fatal != nil:
		return fatal
	case interrupted:
		return errInterrupted
	}
	b.Logf("Stopped.")
	return nil
}
