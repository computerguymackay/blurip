package makemkv

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeMakeMKV writes a script that records its arguments, prints fixture and
// exits with code.
func fakeMakeMKV(t *testing.T, fixture string, code int) (bin, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	abs, err := filepath.Abs("testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	argsFile = filepath.Join(dir, "args")
	bin = filepath.Join(dir, "makemkvcon")
	script := "#!/bin/sh\necho \"$@\" > " + argsFile + "\ncat " + abs + "\nexit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile
}

func TestRunInfo(t *testing.T) {
	bin, argsFile := fakeMakeMKV(t, "bluray-synthetic.txt", 0)
	var raw bytes.Buffer
	info, err := RunInfo(context.Background(), bin, DeviceSource("/dev/sr1"), &raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Titles) != 3 {
		t.Errorf("titles = %d", len(info.Titles))
	}
	args, _ := os.ReadFile(argsFile)
	if got := strings.TrimSpace(string(args)); got != "-r --cache=1 info dev:/dev/sr1" {
		t.Errorf("args = %q", got)
	}
	want, _ := os.ReadFile("testdata/bluray-synthetic.txt")
	if !bytes.Equal(raw.Bytes(), want) {
		t.Error("raw output does not match makemkvcon output")
	}
}

func TestRunInfoNonZeroExitWithOutput(t *testing.T) {
	bin, _ := fakeMakeMKV(t, "drives.txt", 1)
	info, err := RunInfo(context.Background(), bin, ListDrivesSource, nil)
	if err != nil {
		t.Fatalf("non-zero exit with output should not fail: %v", err)
	}
	if len(info.Drives) != 4 {
		t.Errorf("drives = %d", len(info.Drives))
	}
}

func TestRunInfoNoOutput(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "makemkvcon")
	os.WriteFile(bin, []byte("#!/bin/sh\necho boom >&2\nexit 3\n"), 0o755)
	if _, err := RunInfo(context.Background(), bin, ListDrivesSource, nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want error mentioning stderr", err)
	}
}

// A wrapper script (like `flatpak run`) that starts the real makemkvcon as a
// child: on a timeout the child must be killed too, or it keeps the drive.
func TestRunInfoTimeoutKillsChildren(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	bin := filepath.Join(dir, "makemkvcon")
	script := "#!/bin/sh\nsleep 60 &\necho $! > " + pidFile + "\nwait\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := RunInfo(ctx, bin, DeviceSource("/dev/sr0"), nil); err == nil {
		t.Fatal("expected an error")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("took %s", d)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("the wrapper's child was left running")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
