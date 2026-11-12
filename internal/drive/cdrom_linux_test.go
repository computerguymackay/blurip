//go:build linux

package drive

import "testing"

func TestCDROMMissingDevice(t *testing.T) {
	if _, err := (CDROM{}).Status("/dev/blurip-does-not-exist"); err == nil {
		t.Error("expected error for missing device")
	}
	if err := (CDROM{}).Eject("/dev/blurip-does-not-exist"); err == nil {
		t.Error("expected error for missing device")
	}
}

func TestCDROMNotADrive(t *testing.T) {
	// /dev/null opens fine but rejects CD-ROM ioctls.
	if _, err := (CDROM{}).Status("/dev/null"); err == nil {
		t.Error("expected error for a non-CD device")
	}
}
