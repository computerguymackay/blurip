package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// readKeys puts the terminal f into single-key mode (no Enter needed, keys
// not echoed; Ctrl-C still works) and sends each key pressed. restore puts
// the terminal back; call it before asking any questions and on exit. If f
// isn't a terminal, keys is nil.
func readKeys(f *os.File) (keys <-chan byte, restore func()) {
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, func() {}
	}
	raw := *old
	raw.Lflag &^= unix.ICANON | unix.ECHO
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &raw); err != nil {
		return nil, func() {}
	}
	ch := make(chan byte, 8)
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := f.Read(buf)
			if err != nil {
				close(ch)
				return
			}
			if n == 1 {
				select {
				case ch <- buf[0]:
				default: // nobody listening; drop it
				}
			}
		}
	}()
	return ch, func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, old) }
}

// inputGuard keeps what is typed on a terminal from reaching the screen
// or the next question while blurip watches the drives: a key pressed to
// wake the PC would otherwise be echoed into the panel (spaces and new
// lines that smear it) and wait there to answer the next disc's question.
// Echo is off while the panel is up and back on for questions, with what
// was typed meanwhile thrown away. A nil *inputGuard does nothing.
type inputGuard struct {
	fd  int
	old unix.Termios
}

// guardInput returns the guard for terminal f, with echo off (nil if f
// isn't a terminal).
func guardInput(f *os.File) *inputGuard {
	if f == nil {
		return nil
	}
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil
	}
	g := &inputGuard{fd: fd, old: *old}
	g.quiet()
	return g
}

// quiet turns echo off (the panel is up).
func (g *inputGuard) quiet() {
	if g == nil {
		return
	}
	t := g.old
	t.Lflag &^= unix.ECHO
	_ = unix.IoctlSetTermios(g.fd, unix.TCSETS, &t)
}

// ask throws away what was typed while the panel was up and turns echo
// back on, for a question.
func (g *inputGuard) ask() {
	if g == nil {
		return
	}
	_ = unix.IoctlSetInt(g.fd, unix.TCFLSH, unix.TCIFLUSH)
	g.restore()
}

// restore puts the terminal back as it was.
func (g *inputGuard) restore() {
	if g == nil {
		return
	}
	_ = unix.IoctlSetTermios(g.fd, unix.TCSETS, &g.old)
}
