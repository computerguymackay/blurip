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
