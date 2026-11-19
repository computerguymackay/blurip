// Package space decides whether there is room for a rip, accounting for the
// configured fill limit and for rips already in progress.
package space

import (
	"fmt"
	"sync"

	"github.com/dustin/go-humanize"
	"golang.org/x/sys/unix"
)

// Usage describes a file system.
type Usage struct {
	Total uint64 // bytes
	Free  uint64 // bytes available to unprivileged users
}

// Used returns the bytes in use (including the root-reserved area).
func (u Usage) Used() uint64 { return u.Total - u.Free }

// Stat returns the usage of the file system holding path.
func Stat(path string) (Usage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Usage{}, fmt.Errorf("checking free space on %s: %w", path, err)
	}
	return Usage{Total: st.Blocks * uint64(st.Bsize), Free: st.Bavail * uint64(st.Bsize)}, nil
}

// NoRoomError explains why a rip (or an encode) was refused.
type NoRoomError struct {
	What             string // "rip" (if empty) or "encode"
	Needed, Reserved uint64
	Usage            Usage
	MaxFillPercent   int
	OverFillLimit    bool // false: simply not enough free space
}

func (e *NoRoomError) Error() string {
	b := func(n uint64) string { return humanize.IBytes(n) }
	what := e.What
	if what == "" {
		what = "rip"
	}
	extra := ""
	if e.Reserved > 0 {
		extra = fmt.Sprintf(" plus %s reserved by rips in progress", b(e.Reserved))
	}
	if !e.OverFillLimit {
		return fmt.Sprintf("not enough space: %s needs about %s%s, only %s free", what, b(e.Needed), extra, b(e.Usage.Free))
	}
	after := float64(e.Usage.Used()+e.Reserved+e.Needed) / float64(e.Usage.Total) * 100
	return fmt.Sprintf("not enough space: %s needs about %s%s, which would fill the disk to %.0f%% (limit %d%%)",
		what, b(e.Needed), extra, after, e.MaxFillPercent)
}

// Check returns a *NoRoomError if writing needed more bytes (on top of
// reserved bytes already promised to other rips) would exceed the free space
// or push usage past maxFillPercent.
func Check(u Usage, needed, reserved uint64, maxFillPercent int) error {
	e := &NoRoomError{Needed: needed, Reserved: reserved, Usage: u, MaxFillPercent: maxFillPercent}
	if needed+reserved > u.Free {
		return e
	}
	limit := u.Total / 100 * uint64(maxFillPercent)
	if u.Used()+reserved+needed > limit {
		e.OverFillLimit = true
		return e
	}
	return nil
}

// Reservations tracks space promised to rips in progress, so parallel rips
// cannot each pass the check and then fill the disk together.
type Reservations struct {
	mu       sync.Mutex
	reserved uint64
}

// Reserve checks for room on the file system holding path and, if there is
// room, reserves needed bytes until release is called.
func (r *Reservations) Reserve(path string, needed uint64, maxFillPercent int) (release func(), err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, err := Stat(path)
	if err != nil {
		return nil, err
	}
	// Bytes already written by in-progress rips appear in u.Used() and are
	// also still counted in r.reserved, so this errs on the safe side.
	if err := Check(u, needed, r.reserved, maxFillPercent); err != nil {
		return nil, err
	}
	r.reserved += needed
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			r.reserved -= needed
			r.mu.Unlock()
		})
	}, nil
}
