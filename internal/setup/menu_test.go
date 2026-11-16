package setup

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/handbrake"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/store"
)

func menuConfig(t *testing.T) (config.Config, []store.Drive) {
	c := config.Default()
	c.MakeMKVCon, c.OutputDir, c.SetupComplete = "/usr/bin/makemkvcon", t.TempDir(), true
	c.HandBrakeCLI, c.PresetDVD, c.PresetBluRay = handbrake.FlatpakCommand, "General/Fast 1080p30", "General/Fast 1080p30"
	c.MoviesDir, c.ShowsDir, c.DeleteAfterEncode = t.TempDir(), t.TempDir(), true
	return c, []store.Drive{{Device: "/dev/sr1", Name: "BD reader", Nickname: "Blu-ray drive", Enabled: true}}
}

func runMenu(t *testing.T, cur config.Config, drives []store.Drive, encodeOnly bool, input string) (Result, bool, string) {
	t.Helper()
	var out bytes.Buffer
	res, changed, err := Menu(context.Background(), prompt.NewPrompter(strings.NewReader(input), &out),
		testEnv(t, sr1), encodeEnv(t), cur, drives, encodeOnly)
	if err != nil {
		t.Fatalf("%v\n%s", err, &out)
	}
	return res, changed, out.String()
}

func TestMenuChangesOneSetting(t *testing.T) {
	cur, drives := menuConfig(t)
	// 4 = Eject after ripping → no; then Enter saves.
	res, changed, out := runMenu(t, cur, drives, false, "4\nn\n\n")
	if !changed || res.Config.Eject {
		t.Errorf("changed %v, eject %v\n%s", changed, res.Config.Eject, out)
	}
	want := cur
	want.Eject = false
	if res.Config != want {
		t.Errorf("other settings changed:\n got %+v\nwant %+v", res.Config, want)
	}
	if len(res.Drives) != 1 || res.Drives[0].Nickname != "Blu-ray drive" {
		t.Errorf("drives = %+v", res.Drives)
	}
	for _, s := range []string{"  Ripping", " 4) Eject after ripping", "yes", "Encoding with HandBrake", "Preset for Blu-rays", "General / Fast 1080p30"} {
		if !strings.Contains(out, s) {
			t.Errorf("menu lacks %q:\n%s", s, out)
		}
	}
}

func TestMenuQuitWithoutSaving(t *testing.T) {
	cur, drives := menuConfig(t)
	res, changed, _ := runMenu(t, cur, drives, false, "4\nn\nq\n")
	if changed || res.Config != cur {
		t.Errorf("changed %v, config %+v", changed, res.Config)
	}
	if _, changed, _ := runMenu(t, cur, drives, false, "\n"); changed {
		t.Error("Enter straight away: nothing changed")
	}
}

func TestMenuEncodeOnlyPreset(t *testing.T) {
	cur, drives := menuConfig(t)
	// 3 = Preset for Blu-rays → My Presets (1) → 720pFast (1); Enter saves.
	res, changed, out := runMenu(t, cur, drives, true, "3\n1\n1\n\n")
	if !changed || res.Config.PresetBluRay != "My Presets/720pFast" || res.Config.PresetDVD != cur.PresetDVD {
		t.Errorf("config = %+v\n%s", res.Config, out)
	}
	if strings.Contains(out, "MakeMKV") {
		t.Errorf("encode-only menu lists ripping settings:\n%s", out)
	}
}

func TestMenuEncodingNotSetUp(t *testing.T) {
	cur, drives := menuConfig(t)
	cur.HandBrakeCLI = ""
	_, changed, out := runMenu(t, cur, drives, false, "\n")
	if changed || !strings.Contains(out, "7) Encoding") || !strings.Contains(out, "not set up yet") {
		t.Errorf("output:\n%s", out)
	}
}
