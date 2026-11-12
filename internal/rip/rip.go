// Package rip rips one title to the output folder: space check, rip into a
// temporary folder, then move the result into place.
package rip

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/space"
)

// TempDirName is the folder inside the output folder where rips in
// progress are written.
const TempDirName = ".blurip-tmp"

// Request describes one rip.
type Request struct {
	MakeMKVCon     string
	Device         string // e.g. /dev/sr1
	Title          makemkv.Title
	OutputDir      string // root output folder
	Name           string // final folder (and file) name, see FolderName
	JobID          string // unique; names the temp folder
	MaxFillPercent int
	Reservations   *space.Reservations // shared by concurrent rips; nil = private
	OnProgress     func(makemkv.Progress)
	OnMessage      func(makemkv.Message)
}

// Result is a finished rip.
type Result struct {
	Dir   string   // final folder
	Files []string // MKV files inside it
	Bytes int64
}

// ErrNoOutput means makemkvcon exited successfully but wrote no MKV file.
var ErrNoOutput = errors.New("makemkvcon finished but produced no MKV file")

// TempDir returns the temp folder for a job.
func TempDir(outputDir, jobID string) string {
	return filepath.Join(outputDir, TempDirName, jobID)
}

// Run performs the rip. On any error, including cancellation, the temp
// folder is removed and nothing is left in the output folder. Cancellation
// returns ctx.Err().
func Run(ctx context.Context, req Request) (Result, error) {
	if req.Name == "" || req.JobID == "" {
		return Result{}, errors.New("rip: Name and JobID are required")
	}
	res := req.Reservations
	if res == nil {
		res = &space.Reservations{}
	}
	release, err := res.Reserve(req.OutputDir, uint64(max(req.Title.SizeBytes(), 0)), req.MaxFillPercent)
	if err != nil {
		return Result{}, err
	}
	defer release()

	tmp := TempDir(req.OutputDir, req.JobID)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return Result{}, fmt.Errorf("creating temp folder: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(tmp)
		}
	}()

	_, err = makemkv.Rip(ctx, makemkv.RipOptions{
		Bin: req.MakeMKVCon, Source: makemkv.DeviceSource(req.Device), TitleID: req.Title.ID,
		OutDir: tmp, OnProgress: req.OnProgress, OnMessage: req.OnMessage,
	})
	if err != nil {
		return Result{}, err
	}

	files, err := mkvFiles(tmp)
	if err != nil {
		return Result{}, err
	}
	if len(files) == 0 {
		return Result{}, ErrNoOutput
	}
	// A single file is named after the folder: "Wild Card/Wild Card.mkv".
	if len(files) == 1 {
		want := req.Name + ".mkv"
		if err := os.Rename(filepath.Join(tmp, files[0]), filepath.Join(tmp, want)); err != nil {
			return Result{}, err
		}
		files[0] = want
	}

	dest, err := moveIntoPlace(tmp, req.OutputDir, req.Name)
	if err != nil {
		return Result{}, err
	}
	ok = true

	var total int64
	for _, f := range files {
		if fi, err := os.Stat(filepath.Join(dest, f)); err == nil {
			total += fi.Size()
		}
	}
	return Result{Dir: dest, Files: files, Bytes: total}, nil
}

func mkvFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.EqualFold(filepath.Ext(e.Name()), ".mkv") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// moveIntoPlace renames tmp to outputDir/name, adding " (2)", " (3)", ...
// if that folder already exists. Rename never overwrites a non-empty folder
// on Linux, so an existing rip is never clobbered.
func moveIntoPlace(tmp, outputDir, name string) (string, error) {
	for i := 1; i < 1000; i++ {
		dest := filepath.Join(outputDir, name)
		if i > 1 {
			dest = filepath.Join(outputDir, fmt.Sprintf("%s (%d)", name, i))
		}
		if _, err := os.Lstat(dest); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err := os.Rename(tmp, dest); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue // appeared in the meantime
			}
			return "", fmt.Errorf("moving rip into place: %w", err)
		}
		return dest, nil
	}
	return "", fmt.Errorf("too many existing folders named %q", name)
}

// CleanupTemp removes leftover temp folders (from crashes or kills) except
// those for the given active job IDs.
func CleanupTemp(outputDir string, keep ...string) error {
	root := filepath.Join(outputDir, TempDirName)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	skip := map[string]bool{}
	for _, k := range keep {
		skip[k] = true
	}
	for _, e := range entries {
		if !skip[e.Name()] {
			if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// FolderName picks the output folder name for a disc: MakeMKV's disc name if
// it has one ("Wild Card"), otherwise the cleaned-up volume label
// ("THE_MATRIX" → "The Matrix"), otherwise "Untitled disc".
func FolderName(discName, volumeLabel string) string {
	if n := sanitize(discName); n != "" {
		return n
	}
	label := strings.Join(strings.FieldsFunc(volumeLabel, func(r rune) bool { return r == '_' || r == '-' || unicode.IsSpace(r) }), " ")
	if n := sanitize(titleCase(label)); n != "" {
		return n
	}
	return "Untitled disc"
}

func titleCase(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

// sanitize makes s safe as a single path element.
func sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == 0:
			return '-'
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimLeft(s, ".") // no hidden folders, no "." or ".."
	return strings.TrimSpace(s)
}
