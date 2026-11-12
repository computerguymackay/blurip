package setup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/store"
)

func testEnv(t *testing.T, drives ...makemkv.Drive) Env {
	return Env{
		FindMakeMKV: func(string) (string, error) { return "/usr/bin/makemkvcon", nil },
		Version: func(_ context.Context, p string) (string, error) {
			if p == "/usr/bin/makemkvcon" || p == "/opt/mk" {
				return "MakeMKV v2.0.0 linux(x64-release)", nil
			}
			return "", errors.New("not makemkvcon")
		},
		ListDrives: func(context.Context, string) ([]makemkv.Drive, error) { return drives, nil },
		HomeDir:    t.TempDir(),
	}
}

var (
	sr0 = makemkv.Drive{Device: "/dev/sr0", DriveName: "DVD writer", State: makemkv.DriveEmptyClosed}
	sr1 = makemkv.Drive{Device: "/dev/sr1", DriveName: "BD reader", State: makemkv.DriveInserted}
)

func runWizard(t *testing.T, env Env, input string) (Result, string, error) {
	t.Helper()
	var out bytes.Buffer
	res, err := Run(context.Background(), prompt.NewPrompter(strings.NewReader(input), &out), env, config.Default())
	return res, out.String(), err
}

func TestWizardDefaultsSingleDrive(t *testing.T) {
	env := testEnv(t, sr1)
	// Accept makemkvcon, default output dir (create it), defaults for the rest.
	res, out, err := runWizard(t, env, "\n\ny\n\n\n\n")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	c := res.Config
	wantOut := filepath.Join(env.HomeDir, "Videos", "blurip")
	if c.MakeMKVCon != "/usr/bin/makemkvcon" || c.OutputDir != wantOut || c.RipMode != config.RipDefaults ||
		!c.Eject || c.MaxFillPercent != 80 || c.DriveMode != config.DriveSingle || !c.SetupComplete {
		t.Errorf("config = %+v", c)
	}
	if fi, err := os.Stat(wantOut); err != nil || !fi.IsDir() {
		t.Errorf("output dir not created: %v", err)
	}
	if len(res.Drives) != 1 || res.Drives[0].Device != "/dev/sr1" {
		t.Errorf("drives = %+v", res.Drives)
	}
}

func TestWizardCustomAnswersParallel(t *testing.T) {
	env := testEnv(t, sr0, sr1)
	outDir := filepath.Join(env.HomeDir, "rips")
	os.Mkdir(outDir, 0o755)
	input := strings.Join([]string{
		"n",            // don't use the found makemkvcon
		"/bad/path",    // rejected
		"/opt/mk",      // accepted
		"relative/dir", // rejected: not absolute
		"~/rips",       // accepted (exists)
		"2",            // ask every time
		"maybe", "n",   // eject: invalid, then no
		"150", "90", // fill: invalid, then 90
		"9", "2,1,2", // drives: invalid, then both (deduplicated)
		"parallel", // drive mode by name
		"1",        // max concurrent
	}, "\n") + "\n"
	res, out, err := runWizard(t, env, input)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	c := res.Config
	if c.MakeMKVCon != "/opt/mk" || c.OutputDir != outDir || c.RipMode != config.RipAsk || c.Eject ||
		c.MaxFillPercent != 90 || c.DriveMode != config.DriveParallel || c.MaxConcurrent != 1 {
		t.Errorf("config = %+v", c)
	}
	if len(res.Drives) != 2 || res.Drives[0].Device != "/dev/sr1" || res.Drives[1].Device != "/dev/sr0" {
		t.Errorf("drives = %+v", res.Drives)
	}
	for _, msg := range []string{"not makemkvcon", "full path", "answer y or n", "from 1 to 100", "separated by commas"} {
		if !strings.Contains(out, msg) {
			t.Errorf("output missing %q", msg)
		}
	}
}

func TestWizardNoDrives(t *testing.T) {
	_, _, err := runWizard(t, testEnv(t), "\n\ny\n\n\n\n")
	if err == nil || !strings.Contains(err.Error(), "no optical drives") {
		t.Errorf("err = %v", err)
	}
}

func TestWizardEOFAborts(t *testing.T) {
	_, _, err := runWizard(t, testEnv(t, sr1), "\n")
	if !errors.Is(err, prompt.ErrAborted) {
		t.Errorf("err = %v, want prompt.ErrAborted", err)
	}
}

func TestSave(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, _, err := runWizard(t, testEnv(t, sr1), "\n\ny\n\n\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(ctx, s, res); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(ctx, s)
	if err != nil || c != res.Config {
		t.Errorf("Load = %+v, %v", c, err)
	}
	if d, _ := s.Drives(ctx); len(d) != 1 || d[0].Device != "/dev/sr1" || !d[0].Enabled {
		t.Errorf("drives = %+v", d)
	}
}
