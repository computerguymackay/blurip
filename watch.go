package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/computerguymackay/blurip/internal/awake"
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
	labels   map[string]string // device → "Blu-ray drive (sr1)"
	interval time.Duration
	out      io.Writer // the board draws here; questions are asked here
	ask      *prompt.Prompter
	tty      bool // out is a terminal: draw the live panel
	now      func() time.Time
	// infoTimeout limits reading a disc; 0 = defaultInfoTimeout.
	infoTimeout time.Duration
	// boxSet, if set, treats every disc as part of a TV box set.
	boxSet *boxSetState
	// canAsk: questions can be answered (stdin is a terminal).
	canAsk bool
	// quiet (-s): no questions; every one gets its default answer.
	quiet bool
	// queue (-Q): add finished rips to the encode queue.
	queue bool
	// awake keeps the PC from sleeping while a disc is read or ripped.
	awake *awake.Inhibitor

	// askMu lets only one disc ask questions at a time; the others wait
	// their turn. pending counts name questions still open.
	askMu   sync.Mutex
	pending sync.WaitGroup
	// ejecting counts discs whose eject is being tried again.
	ejecting sync.WaitGroup

	// Set by run, recorded with every job.
	mode       config.DriveMode
	drivesUsed string
	rips       *ripCounter
}

// run returns nil when stopped while idle, errInterrupted if a rip was
// interrupted, or the first fatal error (database, closed input).
func (w *watcher) run(ctx context.Context) error {
	b := newBoard(w.out, w.tty)
	defer b.Close()
	if w.awake != nil {
		w.awake.OnLost = func(err error) { b.Logf("⚠ couldn't keep the PC awake, so it may go to sleep mid-rip: %v", err) }
	}
	err := w.watch(ctx, b)
	if err == nil || errors.Is(err, errInterrupted) {
		w.pending.Wait() // open name questions end with Ctrl-C too
	}
	w.ejecting.Wait() // ejects being retried stop with Ctrl-C
	return err
}

func (w *watcher) watch(ctx context.Context, b *board) error {
	devices := w.devices
	if w.cfg.DriveMode == config.DriveSingle && len(devices) > 1 {
		b.Logf("Drive mode is single but %d drives are enabled; using %s only.", len(devices), w.label(devices[0]))
		devices = devices[:1]
	}
	rows := map[string]*driveRow{}
	for _, d := range devices {
		rows[d] = b.Row(d, w.label(d))
	}
	names := make([]string, len(devices))
	for i, d := range devices {
		names[i] = w.label(d)
	}

	w.drivesUsed, w.rips = strings.Join(names, ", "), &ripCounter{}
	mode, maxRips := "", 0
	switch {
	case len(devices) == 1:
		w.mode = config.DriveSingle
	case w.cfg.DriveMode == config.DriveParallel:
		w.mode = config.DriveParallel
		mode, maxRips = "parallel", len(devices)
		if l := w.cfg.MaxConcurrent; l > 0 && l < len(devices) {
			mode, maxRips = fmt.Sprintf("parallel, up to %d at once", l), l
		}
	default:
		w.mode = config.DriveSequential
		mode, maxRips = "one rip at a time", 1
	}
	watching := strings.Join(names, ", ")
	if mode != "" {
		watching += " (" + mode + ")"
	}
	if w.boxSet != nil {
		watching += " · box set"
	}
	if w.queue {
		watching += " · queueing for encoding"
	}
	b.SetHeader(fmt.Sprintf("blurip · watching %s · Ctrl-C to stop", watching))
	if !w.tty {
		b.Logf("Watching %s. Press Ctrl-C to stop.", watching)
	}
	if maxRips == 0 {
		return w.runSequential(ctx, b, devices, rows)
	}
	return w.runDrives(ctx, b, devices, rows, maxRips)
}

// label returns how a drive is shown: its nickname and device, or the device.
func (w *watcher) label(dev string) string {
	if l := w.labels[dev]; l != "" {
		return l
	}
	return shortDev(dev)
}

func (w *watcher) newDriveWatcher(b *board) *drive.Watcher {
	dw := drive.NewWatcher(w.ctl, w.interval)
	dw.OnError = func(dev string, err error) { b.Logf("⚠ %s: %v", w.label(dev), err) }
	dw.OnEmpty = func(dev string) {
		// The disc left in it (if any) has gone.
		if err := w.store.SetDriveLeftDisc(context.Background(), dev, 0); err != nil {
			b.Logf("⚠ %s: couldn't record that it's empty: %v", w.label(dev), err)
		}
	}
	return dw
}

func (w *watcher) session(row *driveRow) *session {
	return &session{cfg: w.cfg, store: w.store, ctl: w.ctl, ask: w.ask, out: w.out, view: row, now: w.now, infoTimeout: w.infoTimeout,
		mode: w.mode, drivesUsed: w.drivesUsed, rips: w.rips, boxSet: w.boxSet,
		canAsk: w.canAsk && !w.quiet, quiet: w.quiet, pending: &w.pending, queue: w.queue, awake: w.awake,
		ejectLater: true, ejecting: &w.ejecting}
}

// askLock returns the session's askLock for dev: one disc's questions at
// a time, with the panel hidden meanwhile. header says which drive the
// questions are about (when several drives are watched).
func (w *watcher) askLock(b *board, dev string, header bool) func() func() {
	return func() func() {
		w.askMu.Lock()
		b.Pause()
		if header {
			fmt.Fprintf(w.out, "\nQuestions about the disc in %s:\n", w.label(dev))
		}
		return func() { b.Resume(); w.askMu.Unlock() }
	}
}

// runSequential handles one disc at a time in single mode. Nothing else
// is watched while a disc is being handled.
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
		sess.askLock = w.askLock(b, dev, false)
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

// prepared is the outcome of reading a disc: a plan to rip, or nil if
// there is nothing to rip (err says why).
type prepared struct {
	dev  string
	sess *session
	plan *plannedRip
	err  error
}

type jobDone struct {
	dev string
	err error
}

// runDrives handles several drives. Every drive is watched all the time;
// a new disc is read (and any questions asked) as soon as it is inserted,
// even while other discs rip, then waits its turn. At most maxRips discs
// rip at once (sequential mode: 1). A finished disc is ejected straight
// away, so the next one can go in while another drive is still busy.
func (w *watcher) runDrives(ctx context.Context, b *board, devices []string, rows map[string]*driveRow, maxRips int) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	reservations := &space.Reservations{}
	dw := w.newDriveWatcher(b)
	busy := map[string]bool{} // being read, waiting, or ripping: not watched
	awakeRelease := map[string]func(){}
	prepDone := make(chan prepared)
	ripDone := make(chan jobDone)
	var waiting []prepared
	preparing, ripping := 0, 0
	var fatal error
	interrupted := false
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	ctxDone := ctx.Done()

	finished := func(dev string, err error) {
		busy[dev] = false
		if release := awakeRelease[dev]; release != nil {
			release()
			delete(awakeRelease, dev)
		}
		dw.Ignore(dev) // don't pick this disc up again until it's removed
		rows[dev].Idle()
		switch {
		case err == nil, errors.Is(err, errRipFailed):
		case errors.Is(err, errInterrupted):
			interrupted = true
		default:
			if fatal == nil {
				fatal = fmt.Errorf("%s: %w", w.label(dev), err)
				cancel() // stop everything else too
			}
		}
	}
	showWaiting := func() {
		for i, p := range waiting {
			msg := "Ready — rips next"
			if i > 0 {
				msg = fmt.Sprintf("Ready — %d discs ahead of it", i)
			}
			rows[p.dev].Activity(msg)
		}
	}

	for {
		if ctx.Err() == nil {
			var idle []string
			for _, d := range devices {
				if !busy[d] {
					idle = append(idle, d)
				}
			}
			for _, dev := range dw.Poll(idle) {
				busy[dev] = true
				awakeRelease[dev] = w.awake.Hold()
				preparing++
				sess := w.session(rows[dev])
				sess.reservations = reservations
				sess.askLock = w.askLock(b, dev, true)
				go func(dev string) {
					plan, err := sess.prepare(ctx, dev)
					prepDone <- prepared{dev, sess, plan, err}
				}(dev)
			}
			if len(waiting) > 0 && ripping < maxRips {
				next := waiting[0]
				waiting = waiting[1:]
				ripping++
				go func() {
					ripDone <- jobDone{next.dev, next.sess.execute(ctx, next.plan)}
				}()
				showWaiting()
			}
		}
		if ctx.Err() != nil && preparing == 0 && ripping == 0 {
			break
		}
		select {
		case p := <-prepDone:
			preparing--
			if p.plan == nil || ctx.Err() != nil {
				err := p.err
				if err == nil && p.plan != nil {
					err = errInterrupted // read, but stopped before its turn
				}
				finished(p.dev, err)
				continue
			}
			waiting = append(waiting, p)
			if ripping >= maxRips {
				showWaiting()
			}
		case d := <-ripDone:
			ripping--
			finished(d.dev, d.err)
		case <-ticker.C:
		case <-ctxDone:
			ctxDone = nil
			if n := preparing + ripping; n > 0 {
				b.Logf("Stopping %d disc(s)...", n)
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
