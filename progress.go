package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/computerguymackay/blurip/internal/makemkv"
)

// progressTracker turns makemkvcon progress into display text, with a
// time-remaining estimate per task.
type progressTracker struct {
	task      string
	taskStart time.Time
	lastDraw  time.Time
	lastPct   int
	now       func() time.Time
}

func newTracker() *progressTracker { return &progressTracker{lastPct: -1, now: time.Now} }

// Update returns the text to show, whether a live display should redraw
// now (at most 5 times a second), and whether a new 10% step was reached
// (for plain logs).
func (p *progressTracker) Update(pr makemkv.Progress) (text string, redraw, milestone bool) {
	now := p.now()
	if pr.Task != p.task {
		p.task, p.taskStart, p.lastPct = pr.Task, now, -1
	}
	frac := pr.Fraction()
	pct := int(frac * 100)
	text = p.render(pr, frac, now)
	if now.Sub(p.lastDraw) >= 200*time.Millisecond || frac >= 1 {
		p.lastDraw, redraw = now, true
	}
	if p.lastPct < 0 || pct/10 != p.lastPct/10 {
		p.lastPct, milestone = pct, true
	}
	return text, redraw, milestone
}

func (p *progressTracker) render(pr makemkv.Progress, frac float64, now time.Time) string {
	const width = 20
	filled := int(frac * width)
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	task := pr.Task
	if task == "" {
		task = "Working"
	}
	s := fmt.Sprintf("%s [%s] %5.1f%%", task, bar, frac*100)
	if eta, ok := estimate(now.Sub(p.taskStart), frac); ok {
		s += "  ETA " + formatDuration(eta)
	}
	return s
}

// estimate returns the remaining time once there is enough progress to judge.
func estimate(elapsed time.Duration, frac float64) (time.Duration, bool) {
	if frac < 0.01 || frac >= 1 || elapsed < 5*time.Second {
		return 0, false
	}
	return time.Duration(float64(elapsed) * (1 - frac) / frac), true
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	return fmt.Sprintf("%dm%02ds", m, s)
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
