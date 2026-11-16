package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/computerguymackay/blurip/internal/makemkv"
	"golang.org/x/sys/unix"
)

// board is blurip's screen. On a terminal it keeps a panel at the bottom,
// redrawn in place, with one entry per drive:
//
//	blurip · watching sr0, sr1 · Ctrl-C to stop
//	DVD drive (sr0)      waiting for a disc
//	Blu-ray drive (sr1)  Wild Card → /rips/Wild Card/Wild Card.mkv
//	                     Saving to MKV file [██████░░░░░░░░░░░░░░]  31.0%  ETA 33m10s
//
// Only lasting events (a disc finished, failed, was skipped; warnings) are
// printed above the panel, one line each with a timestamp. Every panel line
// is cut to the terminal width so it never wraps, which is what keeps the
// panel from smearing down the screen.
//
// When output is not a terminal, the panel is not drawn; activity changes
// and every 10% of progress are written as plain log lines instead.
type board struct {
	mu     sync.Mutex
	w      io.Writer
	tty    bool
	width  func() int
	now    func() time.Time
	header string
	rows   []*driveRow
	drawn  int  // panel lines currently on screen
	paused bool // questions are being asked: panel hidden, others' logs queued
	queued []string
}

func newBoard(w io.Writer, tty bool) *board {
	b := &board{w: w, tty: tty, now: time.Now, width: func() int { return 80 }}
	if f, ok := w.(*os.File); ok && tty {
		b.width = func() int { return terminalWidth(f) }
	}
	return b
}

func terminalWidth(f *os.File) int {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col < 20 {
		return 80
	}
	return int(ws.Col)
}

// SetHeader sets the panel's first line.
func (b *board) SetHeader(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.header = s
	b.redrawLocked()
}

// Row adds a panel entry for a drive, shown as label ("Blu-ray drive
// (sr1)"); an empty label shows the device ("sr1").
func (b *board) Row(dev, label string) *driveRow {
	b.mu.Lock()
	defer b.mu.Unlock()
	if label == "" {
		label = shortDev(dev)
	}
	r := &driveRow{b: b, dev: dev, label: label, tracker: newTracker()}
	b.rows = append(b.rows, r)
	b.redrawLocked()
	return r
}

func shortDev(dev string) string { return strings.TrimPrefix(dev, "/dev/") }

// Logf prints a lasting line above the panel.
func (b *board) Logf(format string, a ...any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.logLocked(fmt.Sprintf(format, a...))
}

func (b *board) logLocked(msg string) {
	line := b.now().Format("15:04:05") + "  " + msg
	if b.paused {
		b.queued = append(b.queued, line)
		return
	}
	b.clearLocked()
	fmt.Fprintln(b.w, line)
	b.redrawLocked()
}

// Pause hides the panel while questions are asked; log lines from other
// drives are held back until Resume. A still copy of every drive's entry
// (its disc, activity and progress at that moment) is printed in its
// place, so it is clear which disc is in which drive while answering.
func (b *board) Pause() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clearLocked()
	if b.tty && !b.paused && len(b.rows) > 0 {
		width := b.width() - 1
		fmt.Fprintln(b.w)
		for _, l := range b.rowLinesLocked() {
			fmt.Fprintln(b.w, truncate(l, width))
		}
	}
	b.paused = true
}

// Resume prints held-back lines and shows the panel again.
func (b *board) Resume() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.paused = false
	for _, l := range b.queued {
		fmt.Fprintln(b.w, l)
	}
	b.queued = nil
	b.redrawLocked()
}

// Close removes the panel from the screen.
func (b *board) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clearLocked()
	b.rows = nil
	b.header = ""
}

func (b *board) clearLocked() {
	if b.tty && b.drawn > 0 {
		fmt.Fprintf(b.w, "\r\033[%dA\033[J", b.drawn) // up N lines, clear to end of screen
	}
	b.drawn = 0
}

func (b *board) redrawLocked() {
	if !b.tty || b.paused {
		return
	}
	b.clearLocked()
	width := b.width() - 1
	var lines []string
	if b.header != "" {
		lines = append(lines, b.header)
	}
	lines = append(lines, b.rowLinesLocked()...)
	for _, l := range lines {
		fmt.Fprintln(b.w, truncate(l, width))
	}
	b.drawn = len(lines)
}

// rowLinesLocked returns every drive's panel lines, labels lined up.
func (b *board) rowLinesLocked() []string {
	pad := 0
	for _, r := range b.rows {
		pad = max(pad, utf8.RuneCountInString(r.label))
	}
	var lines []string
	for _, r := range b.rows {
		lines = append(lines, r.panelLines(pad)...)
	}
	return lines
}

// truncate cuts s to at most n characters, marking the cut with "…".
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n <= 1 {
		return string([]rune(s)[:max(n, 0)])
	}
	return string([]rune(s)[:n-1]) + "…"
}

// driveRow is one drive's entry on the board. It is what a session reports to.
type driveRow struct {
	b                      *board
	dev, label             string
	disc, target, activity string
	progress               string
	tracker                *progressTracker
	idle                   string // shown when there's nothing to do; "" = "waiting for a disc"
}

func (r *driveRow) panelLines(pad int) []string {
	name := r.label + strings.Repeat(" ", pad-utf8.RuneCountInString(r.label))
	indent := strings.Repeat(" ", pad)
	if r.disc == "" && r.activity == "" {
		idle := r.idle
		if idle == "" {
			idle = "waiting for a disc"
		}
		return []string{name + "  " + idle}
	}
	first := name + "  " + orUnknown(r.disc)
	if r.disc == "" {
		first = name + "  disc inserted"
	}
	if r.target != "" {
		first += " → " + r.target
	}
	lines := []string{first}
	// Keep the activity ("Columbo - S06E02 (2 of 3, 1h10m)") on screen
	// above the bar, so it is clear which title the bar belongs to.
	for _, s := range []string{r.activity, r.progress} {
		if s != "" {
			lines = append(lines, indent+"  "+s)
		}
	}
	return lines
}

// SetDisc shows which disc is in the drive and where it is being ripped to.
func (r *driveRow) SetDisc(name, target string) {
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	changed := name != r.disc || target != r.target
	r.disc, r.target = name, target
	if !r.b.tty && changed && target != "" {
		r.b.logLocked(fmt.Sprintf("%s  %s → %s", r.label, orUnknown(name), target))
	}
	r.b.redrawLocked()
}

// Activity shows what the drive is doing now ("Reading disc...").
func (r *driveRow) Activity(text string) {
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	r.activity, r.progress = text, ""
	if !r.b.tty {
		r.b.logLocked(r.label + "  " + text)
	}
	r.b.redrawLocked()
}

// Progress updates the progress bar.
func (r *driveRow) Progress(p makemkv.Progress) {
	text, redraw, milestone := r.tracker.Update(p)
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	r.progress = text
	switch {
	case r.b.tty && redraw:
		r.b.redrawLocked()
	case !r.b.tty && milestone:
		r.b.logLocked(r.label + "  " + text)
	}
}

// ProgressDone clears the progress bar (e.g. between titles).
func (r *driveRow) ProgressDone() {
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	r.progress = ""
	r.tracker = newTracker()
	r.b.redrawLocked()
}

// SetIdle sets what the entry shows when there's nothing to do.
func (r *driveRow) SetIdle(text string) {
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	r.idle = text
	r.b.redrawLocked()
}

// Log prints a lasting line for this drive above the panel.
func (r *driveRow) Log(format string, a ...any) {
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	r.b.logLocked(r.label + "  " + fmt.Sprintf(format, a...))
}

// Idle returns the entry to "waiting for a disc".
func (r *driveRow) Idle() {
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	r.disc, r.target, r.activity, r.progress = "", "", "", ""
	r.tracker = newTracker()
	r.b.redrawLocked()
}
