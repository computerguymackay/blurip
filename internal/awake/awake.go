// Package awake stops the computer going to sleep while blurip is busy:
// a PC left to rip or encode overnight would otherwise suspend after its
// idle time and stop. It takes a systemd-logind inhibitor lock
// (systemd-inhibit) and asks the desktop (KDE, GNOME) not to suspend,
// since desktops don't honour logind locks held by their own user.
package awake

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// Inhibitor holds one lock while anything holds it. A nil *Inhibitor does
// nothing.
type Inhibitor struct {
	Why string // shown by `systemd-inhibit --list`, e.g. "Ripping discs"
	// OnLost is called for each lock that couldn't be taken or went away
	// while held; the computer may then sleep.
	OnLost func(error)

	// start runs the lock holder; tests replace it.
	start func(why string) (stop func(), lost <-chan error, err error)

	mu   sync.Mutex
	n    int
	stop func()
}

// New returns an Inhibitor using systemd-inhibit and the desktop's own
// inhibit service (see desktopInhibit).
func New(why string, onLost func(error)) *Inhibitor {
	return &Inhibitor{Why: why, OnLost: onLost, start: both}
}

// NewFunc returns an Inhibitor that, instead of taking a real lock, calls
// held(true) when it would take one and held(false) when it lets it go
// (for tests).
func NewFunc(held func(bool)) *Inhibitor {
	return &Inhibitor{start: func(string) (func(), <-chan error, error) {
		held(true)
		lost := make(chan error)
		return func() { held(false); close(lost) }, lost, nil
	}}
}

// Hold keeps the computer awake until the returned function is called
// (calling it more than once is harmless). Holds nest: the lock is let go
// when the last one is released.
func (i *Inhibitor) Hold() (release func()) {
	if i == nil {
		return func() {}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	if i.n == 1 {
		i.take()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			i.mu.Lock()
			defer i.mu.Unlock()
			i.n--
			if i.n == 0 && i.stop != nil {
				i.stop()
				i.stop = nil
			}
		})
	}
}

// take starts the lock holder. Call with mu held.
func (i *Inhibitor) take() {
	stop, lost, err := i.start(i.Why)
	if err != nil {
		i.lost(err)
		return
	}
	i.stop = stop
	go func() {
		for err := range lost {
			if err != nil {
				i.lost(err)
			}
		}
	}()
}

func (i *Inhibitor) lost(err error) {
	if i.OnLost != nil {
		i.OnLost(err)
	}
}

// systemdInhibit runs `systemd-inhibit ... cat` with a pipe as cat's
// input: closing the pipe ends cat and so the lock, and if blurip dies
// the pipe closes by itself, so a lock is never left behind.
func systemdInhibit(why string) (func(), <-chan error, error) {
	path, err := exec.LookPath("systemd-inhibit")
	if err != nil {
		return nil, nil, errors.New("systemd-inhibit isn't installed")
	}
	cmd := exec.Command(path, "--what=sleep:idle", "--who=blurip", "--why="+why, "--mode=block", "cat")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("systemd-inhibit: %w", err)
	}
	var mu sync.Mutex
	stopped := false
	lost := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		mu.Lock()
		defer mu.Unlock()
		if !stopped {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" && err != nil {
				msg = err.Error()
			} else if msg == "" {
				msg = "it stopped"
			}
			lost <- fmt.Errorf("systemd-inhibit: %s", msg)
		}
		close(lost)
	}()
	stop := func() {
		mu.Lock()
		stopped = true
		mu.Unlock()
		_ = stdin.Close()
	}
	return stop, lost, nil
}
