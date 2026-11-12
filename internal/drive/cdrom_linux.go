//go:build linux

package drive

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// ioctl request numbers from linux/cdrom.h.
const (
	cdromEject       = 0x5309
	cdromDriveStatus = 0x5326
	cdromLockDoor    = 0x5329
	cdslCurrent      = 0x7fffffff // CDSL_CURRENT (INT_MAX)
)

// CDROM controls drives through the Linux CD-ROM driver.
//
// UseEjectCommand makes Eject work around failures (unmount a desktop
// auto-mount, then try the `eject` program). Trace, if set, receives a line
// for every step Eject takes, for diagnosing drives that won't eject.
type CDROM struct {
	UseEjectCommand bool
	Trace           func(format string, a ...any)
}

func (c CDROM) trace(format string, a ...any) {
	if c.Trace != nil {
		c.Trace(format, a...)
	}
}

func open(device string) (int, error) {
	// O_NONBLOCK lets the open succeed with no disc in the drive.
	fd, err := unix.Open(device, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.EACCES) {
			return -1, fmt.Errorf("opening %s: %w (is your user in the drive's group, usually \"cdrom\"?)", device, err)
		}
		return -1, fmt.Errorf("opening %s: %w", device, err)
	}
	return fd, nil
}

func ioctl(fd int, req, arg uintptr) (int, error) {
	r, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, arg)
	if errno != 0 {
		return -1, errno
	}
	return int(r), nil
}

// Status returns the drive's current status.
func (CDROM) Status(device string) (Status, error) {
	fd, err := open(device)
	if err != nil {
		return NoInfo, err
	}
	defer unix.Close(fd)
	r, err := ioctl(fd, cdromDriveStatus, cdslCurrent)
	if err != nil {
		return NoInfo, fmt.Errorf("%s: drive status: %w", device, err)
	}
	return Status(r), nil
}

// Eject unlocks the tray and ejects the disc.
//
// If that fails and UseEjectCommand is set, it works around the usual
// causes: a disc auto-mounted by the desktop is unmounted (with udisksctl,
// which normal users are allowed to use for such mounts) and the eject is
// retried; failing that, the eject program is tried. The returned error
// explains what was tried. It wraps ErrBusy if the drive was busy.
func (c CDROM) Eject(device string) error {
	err := c.ejectIoctl(device)
	if err == nil {
		c.trace("eject request: accepted")
		return nil
	}
	c.trace("eject request: %v", err)
	var notes []string
	if errors.Is(err, ErrBusy) {
		if h := holders(device); len(h) > 0 {
			notes = append(notes, "the drive is open in "+strings.Join(h, ", "))
			c.trace("drive is open in: %s", strings.Join(h, ", "))
		} else {
			c.trace("no process of yours has the drive open (other users' processes can't be checked without root)")
		}
	}
	if !c.UseEjectCommand {
		return withNotes(err, notes)
	}
	if mounts := mountPoints(device); len(mounts) > 0 {
		where := strings.Join(mounts, ", ")
		c.trace("disc is mounted at %s; unmounting", where)
		if uerr := unmount(device); uerr != nil {
			c.trace("unmount: %v", uerr)
			notes = append(notes, fmt.Sprintf("the disc is mounted at %s and unmounting it failed: %v", where, uerr))
		} else if err = c.ejectIoctl(device); err == nil {
			c.trace("unmounted; eject request: accepted")
			return nil
		} else {
			c.trace("unmounted; eject request: %v", err)
			notes = append(notes, "unmounted "+where+" but eject still failed")
		}
	} else {
		c.trace("disc is not mounted")
	}
	if path, lerr := exec.LookPath("eject"); lerr == nil {
		out, cerr := exec.Command(path, device).CombinedOutput()
		if cerr == nil {
			c.trace("eject program: accepted")
			return nil
		}
		c.trace("eject program: %v %s", cerr, bytes.TrimSpace(out))
		notes = append(notes, fmt.Sprintf("eject program: %v %s", cerr, bytes.TrimSpace(out)))
	} else {
		c.trace("eject program: not installed")
	}
	return withNotes(err, notes)
}

func withNotes(err error, notes []string) error {
	if len(notes) == 0 {
		return err
	}
	return fmt.Errorf("%w (%s)", err, strings.Join(notes, "; "))
}

// holders lists the processes (visible to this user) that have device open,
// e.g. "gvfsd-udisks2 (pid 1234)". The CD-ROM driver refuses to eject while
// any other process has the drive open.
func holders(device string) []string {
	want := resolve(device)
	fds, _ := filepath.Glob("/proc/[0-9]*/fd/*")
	self := strconv.Itoa(os.Getpid())
	seen := map[string]bool{}
	var out []string
	for _, fd := range fds {
		target, err := os.Readlink(fd)
		if err != nil || (target != device && target != want) {
			continue
		}
		pid := strings.Split(fd, "/")[2]
		if pid == self || seen[pid] {
			continue
		}
		seen[pid] = true
		comm, _ := os.ReadFile("/proc/" + pid + "/comm")
		out = append(out, fmt.Sprintf("%s (pid %s)", strings.TrimSpace(string(comm)), pid))
	}
	return out
}

// unmount unmounts device, preferring udisksctl (works without root for
// desktop auto-mounts) over umount.
func unmount(device string) error {
	cmd := exec.Command("umount", device)
	if path, err := exec.LookPath("udisksctl"); err == nil {
		cmd = exec.Command(path, "unmount", "--no-user-interaction", "-b", device)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// mountPoints returns where device is mounted, if anywhere.
func mountPoints(device string) []string {
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseMounts(f, device)
}

func parseMounts(r io.Reader, device string) []string {
	want := resolve(device)
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && (f[0] == device || resolve(f[0]) == want) {
			out = append(out, unescapeMount(f[1]))
		}
	}
	return out
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// unescapeMount decodes the octal escapes (\040 = space) used in /proc/mounts.
func unescapeMount(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func (CDROM) ejectIoctl(device string) error {
	fd, err := open(device)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	_, _ = ioctl(fd, cdromLockDoor, 0) // unlock; harmless if it fails
	if _, err := ioctl(fd, cdromEject, 0); err != nil {
		if errors.Is(err, unix.EBUSY) {
			return fmt.Errorf("%s: %w", device, ErrBusy)
		}
		return fmt.Errorf("%s: eject: %w", device, err)
	}
	return nil
}

var _ Controller = CDROM{}
