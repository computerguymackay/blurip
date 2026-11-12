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
		DriveMode: DriveParallel, MaxConcurrent: 2, SetupComplete: true,
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
