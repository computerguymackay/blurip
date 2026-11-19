package rip

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLooksLikeCode(t *testing.T) {
	for name, want := range map[string]bool{
		"SBK0X7AJ": true, "D1234567": true, "DVD_VIDEO": true, "DVD VIDEO": true, "BDROM": true,
		"Untitled disc": true, "Disc 1": true, "20231115": true, "MKD34X7 D1": true, "": true, "LOGICAL_VOLUME_ID": true,
		"Wild Card": false, "The Predator": false, "1917": false, "300": false, "Killjoys S1D1": false,
		"The Lost World - Jurassic Park": false, "Columbo Season6and7 Disc1": false, "Alien 3": false,
		"EN_100613": true, "UK_PAL_D1": true, "It": false, "Us": false, "Wild Card UK": false,
	} {
		if got := LooksLikeCode(name); got != want {
			t.Errorf("LooksLikeCode(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestRename(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "SBK0X7AJ")
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, "SBK0X7AJ.mkv"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(root, "Wild Card"), 0o755) // taken: never merged into
	os.WriteFile(filepath.Join(root, "Wild Card", "Wild Card.mkv"), []byte("old"), 0o644)

	res, err := Rename(Result{Name: "SBK0X7AJ", Dir: old, Files: []string{"SBK0X7AJ.mkv"}, Bytes: 1}, "SBK0X7AJ", "Wild Card")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "Wild Card (2)")
	if res.Dir != want || res.Files[0] != "Wild Card.mkv" || res.Name != "Wild Card" {
		t.Errorf("res = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(want, "Wild Card.mkv")); err != nil {
		t.Error(err)
	}
}
