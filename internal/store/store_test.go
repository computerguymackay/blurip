package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "blurip.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestOpenMigratesAndReopens(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	v, err := s.SchemaVersion(ctx)
	if err != nil || v != 8 {
		t.Fatalf("SchemaVersion = %d, %v", v, err)
	}
	if err := s.SetSettings(ctx, map[string]string{"a": "1"}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if got, err := s2.Setting(ctx, "a"); got != "1" || err != nil {
		t.Errorf("Setting(a) = %q, %v", got, err)
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	if _, err := s.db.ExecContext(ctx, "PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(ctx, path); err == nil {
		t.Fatal("expected error opening a newer schema")
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.Setting(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	s.SetSettings(ctx, map[string]string{"a": "1", "b": "2"})
	s.SetSettings(ctx, map[string]string{"a": "3"})
	all, err := s.Settings(ctx)
	if err != nil || all["a"] != "3" || all["b"] != "2" || len(all) != 2 {
		t.Errorf("Settings = %v, %v", all, err)
	}
}

func TestSaveSetupDisablesUnlistedDrives(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if err := s.SaveSetup(ctx, map[string]string{"k": "v"}, []Drive{{Device: "/dev/sr0", Name: "A"}, {Device: "/dev/sr1", Name: "B"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceDrives(ctx, []Drive{{Device: "/dev/sr1", Name: "B2"}}); err != nil {
		t.Fatal(err)
	}
	drives, err := s.Drives(ctx)
	if err != nil || len(drives) != 2 {
		t.Fatalf("Drives = %+v, %v", drives, err)
	}
	if drives[0].Device != "/dev/sr0" || drives[0].Enabled {
		t.Errorf("sr0 = %+v, want disabled", drives[0])
	}
	if drives[1].Device != "/dev/sr1" || !drives[1].Enabled || drives[1].Name != "B2" {
		t.Errorf("sr1 = %+v", drives[1])
	}
}

func TestDefaultPathEnv(t *testing.T) {
	t.Setenv(EnvDB, "/tmp/x.db")
	if p, _ := DefaultPath(); p != "/tmp/x.db" {
		t.Errorf("DefaultPath = %q", p)
	}
	t.Setenv(EnvDB, "")
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	if p, _ := DefaultPath(); p != "/cfg/blurip/blurip.db" {
		t.Errorf("DefaultPath = %q", p)
	}
}

func TestMigrationKeepsDrivesWithoutNickname(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v2.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"migrations/0001_init.sql", "migrations/0002_disc_name.sql"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO drives(device, name, enabled) VALUES('/dev/sr1', 'BD', 1); PRAGMA user_version = 2"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d, err := s.Drives(ctx)
	if err != nil || len(d) != 1 || d[0].Nickname != "" || d[0].Label() != "sr1" || !d[0].Enabled {
		t.Fatalf("drives = %+v, %v", d, err)
	}
}

func TestDriveNicknames(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if err := s.ReplaceDrives(ctx, []Drive{
		{Device: "/dev/sr0", Name: "DVD", Nickname: "DVD drive"},
		{Device: "/dev/sr1", Name: "BD", Nickname: " Blu-ray drive "},
	}); err != nil {
		t.Fatal(err)
	}
	d, err := s.FindDrive(ctx, "blu-ray DRIVE")
	if err != nil || d.Device != "/dev/sr1" || d.Label() != "Blu-ray drive (sr1)" {
		t.Fatalf("FindDrive = %+v, %v", d, err)
	}
	for _, ref := range []string{"sr0", "/dev/sr0", "dvd drive"} {
		if d, err := s.FindDrive(ctx, ref); err != nil || d.Device != "/dev/sr0" {
			t.Errorf("FindDrive(%q) = %+v, %v", ref, d, err)
		}
	}
	if _, err := s.FindDrive(ctx, "drive 9"); !errors.Is(err, ErrUnknownDrive) {
		t.Errorf("FindDrive unknown: %v", err)
	}
	if err := s.SetDriveNickname(ctx, "/dev/sr0", "BLU-RAY DRIVE"); !errors.Is(err, ErrNicknameTaken) {
		t.Errorf("duplicate nickname: %v", err)
	}
	if err := s.SetDriveNickname(ctx, "/dev/sr0", "Drive 1"); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.FindDrive(ctx, "drive 1"); d.Device != "/dev/sr0" {
		t.Errorf("after rename: %+v", d)
	}
	if err := s.SetDriveNickname(ctx, "/dev/sr7", "x"); !errors.Is(err, ErrUnknownDrive) {
		t.Errorf("unknown device: %v", err)
	}
}
