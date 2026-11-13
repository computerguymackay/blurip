package makemkv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fake makemkvcon for `mkv`: $7 is the output directory.
const fakeRipOK = `
echo 'MSG:1005,0,1,"MakeMKV v2.0.0 linux(x64-release) started","%1 started","MakeMKV v2.0.0 linux(x64-release)"'
echo 'PRGT:5018,0,"Saving to MKV file"'
echo 'PRGC:5017,0,"Saving all titles to MKV files"'
echo 'PRGV:0,0,65536'
echo 'PRGV:32768,32768,65536'
printf 'data' > "$7/Wild_Card_t01.mkv"
echo 'PRGV:65536,65536,65536'
echo 'MSG:5036,0,1,"Copy complete. 1 titles saved.","Copy complete. %1 titles saved.","1"'
`

func TestRip(t *testing.T) {
	bin := writeScript(t, fakeRipOK)
	out := t.TempDir()
	var progress []Progress
	msgs, err := Rip(context.Background(), RipOptions{
		Bin: bin, Source: DeviceSource("/dev/sr1"), TitleID: 1, OutDir: out,
		OnProgress: func(p Progress) { progress = append(progress, p) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || !strings.Contains(msgs[1].Text, "1 titles saved") {
		t.Errorf("msgs = %+v", msgs)
	}
	last := progress[len(progress)-1]
	if last.Fraction() != 1 || last.Task != "Saving to MKV file" || last.Operation == "" {
		t.Errorf("last progress = %+v", last)
	}
	if mid := progress[3]; mid.Fraction() != 0.5 {
		t.Errorf("progress[3] = %+v (%.2f)", mid, mid.Fraction())
	}
	if _, err := os.Stat(filepath.Join(out, "Wild_Card_t01.mkv")); err != nil {
		t.Error(err)
	}
}

func TestRipArgs(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	bin := writeScript(t, `echo "$@" > `+argsFile+"\n")
	if _, err := Rip(context.Background(), RipOptions{Bin: bin, Source: "dev:/dev/sr1", TitleID: 3, OutDir: "/out"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(argsFile)
	if strings.TrimSpace(string(got)) != "-r --noscan --progress=-same mkv dev:/dev/sr1 3 /out" {
		t.Errorf("args = %q", got)
	}
}

func TestRipFailure(t *testing.T) {
	bin := writeScript(t, `
echo 'MSG:2003,0,0,"Error reading sector","Error reading sector"'
echo 'MSG:5003,0,0,"Failed to save title 1","Failed to save title %1","1"'
echo 'disc on fire' >&2
exit 2
`)
	_, err := Rip(context.Background(), RipOptions{Bin: bin, Source: "dev:/x", OutDir: t.TempDir()})
	var re *RipError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RipError", err)
	}
	for _, want := range []string{"Error reading sector", "Failed to save title 1", "disc on fire"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestRipCancelKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	// The child sleep is what would keep running if only the parent were killed.
	bin := writeScript(t, "echo 'PRGV:1,1,10'\nsleep 30 &\necho $! > "+pidFile+"\nwait\n")
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	errc := make(chan error, 1)
	go func() {
		_, err := Rip(ctx, RipOptions{Bin: bin, Source: "dev:/x", OutDir: dir, StopGrace: time.Second,
			OnProgress: func(Progress) { started <- struct{}{} }})
		errc <- err
	}()
	<-started
	time.Sleep(50 * time.Millisecond) // let the script record the child's pid
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Rip did not return after cancel")
	}
	pid, _ := os.ReadFile(pidFile)
	p := strings.TrimSpace(string(pid))
	if p == "" {
		return
	}
	// The child was signalled; give it a moment to exit.
	alive := func() bool {
		status, err := os.ReadFile("/proc/" + p + "/stat")
		return err == nil && !strings.Contains(string(status), ") Z")
	}
	for deadline := time.Now().Add(2 * time.Second); alive() && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if alive() {
		t.Errorf("child process %s still running", p)
	}
}
