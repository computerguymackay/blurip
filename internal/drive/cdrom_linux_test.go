//go:build linux

package drive

import (
	"strings"
	"testing"
)

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

func TestParseMounts(t *testing.T) {
	mounts := `/dev/sda1 / ext4 rw 0 0
/dev/sr1 /media/colin/WILD\040CARD udf ro,nosuid 0 0
/dev/sr0 /mnt/other iso9660 ro 0 0
`
	got := parseMounts(strings.NewReader(mounts), "/dev/sr1")
	if len(got) != 1 || got[0] != "/media/colin/WILD CARD" {
		t.Errorf("parseMounts = %q", got)
	}
	if got := parseMounts(strings.NewReader(mounts), "/dev/sr2"); len(got) != 0 {
		t.Errorf("unmounted device: %q", got)
	}
}
