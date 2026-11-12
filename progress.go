package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/computerguymackay/blurip/internal/makemkv"
)

// progressLine renders makemkvcon progress as a single updating line on a
// terminal, or as occasional plain lines when output is redirected.
type progressLine struct {
	out       io.Writer
	tty       bool
	task      string
	taskStart time.Time
	lastDraw  time.Time
	lastPct   int
	now       func() time.Time
}

func newProgressLine(f *os.File) *progressLine {
	fi, err := f.Stat()
	return &progressLine{out: f, tty: err == nil && fi.Mode()&os.ModeCharDevice != 0, lastPct: -1, now: time.Now}
}

func (p *progressLine) Update(pr makemkv.Progress) {
	now := p.now()
	if pr.Task != p.task {
		if p.task != "" && p.tty {
			fmt.Fprintln(p.out)
		}
		p.task, p.taskStart, p.lastPct = pr.Task, now, -1
	}
	frac := pr.Fraction()
	pct := int(frac * 100)
	if p.tty {
		if now.Sub(p.lastDraw) < 200*time.Millisecond && frac < 1 {
			return
		}
		p.lastDraw = now
		fmt.Fprintf(p.out, "\r\033[K%s", p.render(pr, frac, now))
		return
	}
	if pct/10 != p.lastPct/10 || p.lastPct < 0 {
		p.lastPct = pct
		fmt.Fprintln(p.out, p.render(pr, frac, now))
	}
}

// Done ends the progress line.
func (p *progressLine) Done() {
	if p.tty && p.task != "" {
		fmt.Fprintln(p.out)
	}
}

func (p *progressLine) render(pr makemkv.Progress, frac float64, now time.Time) string {
	const width = 30
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
