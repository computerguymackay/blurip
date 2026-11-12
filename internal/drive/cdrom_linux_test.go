//go:build linux

package drive

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestHolders(t *testing.T) {
	f := filepath.Join(t.TempDir(), "fake-sr0")
	os.WriteFile(f, nil, 0o644)
	if h := holders(f); len(h) != 0 {
		t.Fatalf("holders before = %v", h)
	}
	// A child process keeps the "device" open on stdin.
	in, _ := os.Open(f)
	defer in.Close()
	cmd := exec.Command("sleep", "5")
	cmd.Stdin = in
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer cmd.Process.Kill()
	time.Sleep(50 * time.Millisecond)
	h := holders(f)
	want := fmt.Sprintf("sleep (pid %d)", cmd.Process.Pid)
	if len(h) != 1 || h[0] != want {
		t.Errorf("holders = %v, want [%s] (our own process must not be listed)", h, want)
	}
}
