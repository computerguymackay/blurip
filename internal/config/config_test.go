package config

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/computerguymackay/blurip/internal/store"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRoundTrip(t *testing.T) {
	c := Config{
		MakeMKVCon: "/usr/bin/makemkvcon", OutputDir: "/rips", RipMode: RipAsk,
		TitleMode: TitleMainFeature, Eject: false, MaxFillPercent: 90,
		DriveMode: DriveParallel, MaxConcurrent: 2, SetupComplete: true, ReadTimeout: 7,
	}
	got, err := FromMap(c.ToMap())
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Errorf("FromMap(ToMap(c)) = %+v, %v", got, err)
	}
}

func TestFromMapErrors(t *testing.T) {
	for _, m := range []map[string]string{
		{KeyEject: "maybe"},
		{KeyMaxFillPercent: "lots"},
		{KeyMaxFillPercent: "0"},
		{KeyRipMode: "sometimes"},
		{KeyDriveMode: "turbo"},
		{KeyMaxConcurrent: "-1"},
		{KeyReadTimeout: "-1"},
	} {
		if _, err := FromMap(m); err == nil {
			t.Errorf("FromMap(%v) accepted invalid input", m)
		}
	}
}

func TestFromMapIgnoresUnknownKeys(t *testing.T) {
	if _, err := FromMap(map[string]string{"future_setting": "x"}); err != nil {
		t.Error(err)
	}
}

func TestLoadSave(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	c, err := Load(ctx, s)
	if err != nil || !reflect.DeepEqual(c, Default()) {
		t.Fatalf("Load on empty db = %+v, %v", c, err)
	}
	c.OutputDir = "/rips"
	c.MaxFillPercent = 101
	if err := Save(ctx, s, c); err == nil {
		t.Fatal("Save accepted invalid config")
	}
	c.MaxFillPercent = 75
	if err := Save(ctx, s, c); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(ctx, s); !reflect.DeepEqual(got, c) {
		t.Errorf("Load = %+v, want %+v", got, c)
	}
}

func TestDestDir(t *testing.T) {
	c := Default()
	c.OutputDir = "/rips"
	for _, kind := range []string{"DVD", "Blu-ray", ""} {
		if got := c.DestDir(kind); got != "/rips" {
			t.Errorf("not split: DestDir(%q) = %q", kind, got)
		}
	}
	c.SplitByType = true
	for kind, want := range map[string]string{"DVD": "/rips/DVD", "Blu-ray": "/rips/Blu-ray", "": "/rips"} {
		if got := c.DestDir(kind); got != want {
			t.Errorf("split: DestDir(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestSet(t *testing.T) {
	c := Default()
	c, err := c.Set(KeySplitByType, "yes")
	if err != nil || !c.SplitByType {
		t.Fatalf("Set split_by_type yes = %+v, %v", c, err)
	}
	if c, err = c.Set(KeyEject, "off"); err != nil || c.Eject {
		t.Fatalf("Set eject off = %+v, %v", c, err)
	}
	if c, err = c.Set(KeyDriveMode, "parallel"); err != nil || c.DriveMode != DriveParallel {
		t.Fatalf("Set drive_mode = %+v, %v", c, err)
	}
	for _, bad := range [][2]string{{"nope", "1"}, {KeyMaxFillPercent, "101"}, {KeyRipMode, "sometimes"}, {KeyEject, "maybe"}} {
		if _, err := c.Set(bad[0], bad[1]); err == nil {
			t.Errorf("Set(%q, %q) succeeded", bad[0], bad[1])
		}
	}
}
