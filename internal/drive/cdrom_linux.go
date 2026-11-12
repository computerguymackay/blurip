//go:build linux

package drive

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"

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
// UseEjectCommand makes Eject fall back to the `eject` program when the disc
// is mounted (desktop auto-mount), since that program unmounts first.
type CDROM struct {
	UseEjectCommand bool
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

// Eject unlocks the tray and ejects the disc. It returns ErrBusy if the disc
// is in use and could not be ejected.
func (c CDROM) Eject(device string) error {
	err := c.ejectIoctl(device)
	if err == nil || !errors.Is(err, ErrBusy) || !c.UseEjectCommand {
		return err
	}
	// Probably auto-mounted: the eject program unmounts before ejecting.
	path, lerr := exec.LookPath("eject")
	if lerr != nil {
		return err
	}
	if out, cerr := exec.Command(path, device).CombinedOutput(); cerr != nil {
		return fmt.Errorf("%w: eject command: %v: %s", ErrBusy, cerr, bytes.TrimSpace(out))
	}
	return nil
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
