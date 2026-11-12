package makemkv

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Progress is the state reported by PRGV/PRGT/PRGC lines.
type Progress struct {
	Current, Total, Max int    // PRGV: current-operation and overall progress, out of Max
	Task                string // PRGT: overall task, e.g. "Saving to MKV file"
	Operation           string // PRGC: current operation
}

// Fraction returns overall progress from 0 to 1.
func (p Progress) Fraction() float64 {
	if p.Max <= 0 {
		return 0
	}
	f := float64(p.Total) / float64(p.Max)
	if f > 1 {
		return 1
	}
	return f
}

// RipOptions describes one `makemkvcon mkv` run.
type RipOptions struct {
	Bin        string
	Source     string // e.g. DeviceSource("/dev/sr1")
	TitleID    int
	OutDir     string // must exist
	OnProgress func(Progress)
	OnMessage  func(Message)
	// StopGrace is how long makemkvcon gets to exit after SIGTERM before it
	// is killed (default 10s).
	StopGrace time.Duration
}

// RipError is a failed rip, with MakeMKV's last messages as the reason.
type RipError struct {
	Err      error
	Messages []Message
}

func (e *RipError) Error() string {
	var texts []string
	for _, m := range lastN(e.Messages, 3) {
		texts = append(texts, m.Text)
	}
	if len(texts) == 0 {
		return fmt.Sprintf("makemkvcon failed: %v", e.Err)
	}
	return fmt.Sprintf("makemkvcon failed: %v: %s", e.Err, strings.Join(texts, " / "))
}

func (e *RipError) Unwrap() error { return e.Err }

func lastN[T any](s []T, n int) []T {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// Rip runs `makemkvcon -r --noscan --progress=-same mkv <source> <title> <dir>`.
// Cancelling ctx stops makemkvcon (SIGTERM to its process group, then
// SIGKILL) and returns ctx.Err(). It returns all MSG messages seen.
func Rip(ctx context.Context, opt RipOptions) ([]Message, error) {
	if opt.StopGrace == 0 {
		opt.StopGrace = 10 * time.Second
	}
	cmd := exec.Command(opt.Bin, "-r", "--noscan", "--progress=-same",
		"mkv", opt.Source, strconv.Itoa(opt.TitleID), opt.OutDir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting makemkvcon: %w", err)
	}

	// Stop the whole process group when ctx is cancelled.
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-done:
			return
		case <-ctx.Done():
		}
		pgid := cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(opt.StopGrace):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}()

	var msgs []Message
	var prog Progress
	scanErr := scanLines(stdout, func(rec Record) {
		f := rec.Fields
		switch rec.Kind {
		case "MSG":
			if m, ok := parseMessage(f); ok {
				msgs = append(msgs, m)
				if opt.OnMessage != nil {
					opt.OnMessage(m)
				}
			}
		case "PRGV":
			if len(f) >= 3 {
				prog.Current, prog.Total, prog.Max = atoi(f[0]), atoi(f[1]), atoi(f[2])
				if opt.OnProgress != nil {
					opt.OnProgress(prog)
				}
			}
		case "PRGT", "PRGC":
			if len(f) >= 3 {
				if rec.Kind == "PRGT" {
					prog.Task = f[2]
				} else {
					prog.Operation = f[2]
				}
				if opt.OnProgress != nil {
					opt.OnProgress(prog)
				}
			}
		}
	})
	waitErr := cmd.Wait()
	close(done)
	<-stopped

	if ctx.Err() != nil {
		return msgs, ctx.Err()
	}
	if waitErr != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			msgs = append(msgs, Message{Code: -1, Text: s})
		}
		return msgs, &RipError{Err: waitErr, Messages: msgs}
	}
	if scanErr != nil {
		return msgs, fmt.Errorf("reading makemkvcon output: %w", scanErr)
	}
	return msgs, nil
}

// scanLines is like ScanRecords but keeps draining r after a scan error so
// makemkvcon never blocks on a full pipe.
func scanLines(r io.Reader, fn func(Record)) error {
	err := ScanRecords(r, fn)
	if err != nil {
		_, _ = io.Copy(io.Discard, r)
	}
	return err
}

func parseMessage(f []string) (Message, bool) {
	if len(f) < 4 {
		return Message{}, false
	}
	m := Message{Code: atoi(f[0]), Flags: atoi(f[1]), Text: f[3]}
	if len(f) > 4 {
		m.Format = f[4]
		m.Params = f[5:]
	}
	return m, true
}
