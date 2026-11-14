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
	"unicode/utf8"

	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/space"
)

// TempDirName is the folder inside the output folder where rips in
// progress are written.
const TempDirName = ".blurip-tmp"

// Request describes one rip.
type Request struct {
	MakeMKVCon     string
	Device         string          // e.g. /dev/sr1
	Titles         []makemkv.Title // ripped in order into one folder
	OutputDir      string          // root output folder (holds the temp folder)
	DestDir        string          // folder the finished rip goes into; "" = OutputDir. Must be on the same file system as OutputDir.
	Name           string          // final folder (and file) name, see FolderName
	JobID          string          // unique; names the temp folder
	MaxFillPercent int
	Reservations   *space.Reservations // shared by concurrent rips; nil = private
	OnProgress     func(makemkv.Progress)
	OnMessage      func(makemkv.Message)
	OnTitle        func(i, n int, t makemkv.Title) // called before each title (i from 1)

	// FileNames, if set, names each title's file (without .mkv), in the
	// same order as Titles, and the files go straight into DestDir, which
	// may already hold other files (box sets: a season folder that each
	// disc adds to). An existing file is never overwritten: " (2)" is added.
	FileNames []string
}

// EstimatedBytes is MakeMKV's size estimate for all requested titles.
func (r Request) EstimatedBytes() uint64 {
	var n uint64
	for _, t := range r.Titles {
		n += uint64(max(t.SizeBytes(), 0))
	}
	return n
}

// Result is a finished rip.
type Result struct {
	Dir   string   // final folder
	Files []string // MKV files inside it
	Bytes int64
}

// KeepMarker in a temp folder means it holds a finished rip that could not
// be moved into place; CleanupTemp never removes such folders.
const KeepMarker = ".blurip-keep"

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
	if req.Name == "" || req.JobID == "" || len(req.Titles) == 0 {
		return Result{}, errors.New("rip: Name, JobID and at least one title are required")
	}
	if req.FileNames != nil && len(req.FileNames) != len(req.Titles) {
		return Result{}, errors.New("rip: FileNames must have one name per title")
	}
	// Never create the output folder here: if it's missing, the disk it
	// lives on is probably not mounted, and the rip would fill the wrong disk.
	if fi, err := os.Stat(req.OutputDir); err != nil || !fi.IsDir() {
		return Result{}, fmt.Errorf("output folder %s is missing (is its drive mounted?)", req.OutputDir)
	}
	res := req.Reservations
	if res == nil {
		res = &space.Reservations{}
	}
	release, err := res.Reserve(req.OutputDir, req.EstimatedBytes(), req.MaxFillPercent)
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

	seen := map[string]bool{}
	for i, t := range req.Titles {
		if req.OnTitle != nil {
			req.OnTitle(i+1, len(req.Titles), t)
		}
		_, err = makemkv.Rip(ctx, makemkv.RipOptions{
			Bin: req.MakeMKVCon, Source: makemkv.DeviceSource(req.Device), TitleID: t.ID,
			OutDir: tmp, OnProgress: req.OnProgress, OnMessage: req.OnMessage,
		})
		if err != nil {
			if len(req.Titles) > 1 && ctx.Err() == nil {
				return Result{}, fmt.Errorf("title %d: %w", t.ID, err)
			}
			return Result{}, err
		}
		if req.FileNames != nil {
			nameNewFile(tmp, seen, req.FileNames[i])
		}
	}

	files, err := mkvFiles(tmp)
	if err != nil {
		return Result{}, err
	}
	if len(files) == 0 {
		return Result{}, ErrNoOutput
	}
	// The rip has succeeded. From here on nothing may throw it away: if a
	// rename fails, fall back to a plainer name instead of failing.

	// A single file is named after the folder: "Wild Card/Wild Card.mkv".
	// If the file system refuses that name, MakeMKV's name is kept.
	if len(files) == 1 && req.FileNames == nil {
		want := req.Name + ".mkv"
		if err := os.Rename(filepath.Join(tmp, files[0]), filepath.Join(tmp, want)); err == nil {
			files[0] = want
		}
	}

	destDir := req.DestDir
	if destDir == "" {
		destDir = req.OutputDir
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		ok = true
		_ = os.WriteFile(filepath.Join(tmp, KeepMarker), []byte("finished rip that could not be moved into place\n"), 0o644)
		return Result{}, fmt.Errorf("creating %s: %w; the finished rip was kept in %s", destDir, err, tmp)
	}
	if req.FileNames != nil {
		return mergeInto(tmp, destDir, files)
	}
	dest, err := moveIntoPlace(tmp, destDir, req.Name)
	if err != nil {
		// The name itself may be the problem: try a plain ASCII one, then a
		// name that can't fail.
		fallbacks := []string{asciiName(req.Name), "blurip " + req.JobID}
		for _, name := range fallbacks {
			if name == "" || name == req.Name {
				continue
			}
			if d, ferr := moveIntoPlace(tmp, destDir, name); ferr == nil {
				dest, err = d, nil
				break
			}
		}
	}
	if err != nil {
		// Keep the finished rip where it is rather than deleting it, and
		// mark it so startup cleanup leaves it alone.
		ok = true
		_ = os.WriteFile(filepath.Join(tmp, KeepMarker), []byte("finished rip that could not be moved into place\n"), 0o644)
		return Result{}, fmt.Errorf("%w; the finished rip was kept in %s", err, tmp)
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

// nameNewFile renames the MKV file that appeared in dir since the last call
// (tracked in seen) to name.mkv. If the name is refused, MakeMKV's is kept.
func nameNewFile(dir string, seen map[string]bool, name string) {
	files, _ := mkvFiles(dir)
	var fresh []string
	for _, f := range files {
		if !seen[f] {
			fresh = append(fresh, f)
		}
	}
	if len(fresh) == 1 {
		want := name + ".mkv"
		if want != fresh[0] {
			if _, err := os.Lstat(filepath.Join(dir, want)); err == nil {
				want = fresh[0] // already taken in the temp folder
			} else if err := os.Rename(filepath.Join(dir, fresh[0]), filepath.Join(dir, want)); err != nil {
				want = fresh[0]
			}
		}
		fresh[0] = want
	}
	for _, f := range fresh {
		seen[f] = true
	}
}

// mergeInto moves each file from tmp into destDir without overwriting
// anything, then removes tmp. If a file can't be moved, the rest stay in
// tmp, which is marked to keep.
func mergeInto(tmp, destDir string, files []string) (Result, error) {
	res := Result{Dir: destDir}
	for _, f := range files {
		moved, err := moveFile(filepath.Join(tmp, f), destDir, f)
		if err != nil {
			_ = os.WriteFile(filepath.Join(tmp, KeepMarker), []byte("finished rip that could not be moved into place\n"), 0o644)
			return Result{}, fmt.Errorf("moving %s into %s: %w; the rest of the rip was kept in %s", f, destDir, err, tmp)
		}
		res.Files = append(res.Files, moved)
		if fi, err := os.Stat(filepath.Join(destDir, moved)); err == nil {
			res.Bytes += fi.Size()
		}
	}
	os.RemoveAll(tmp)
	return res, nil
}

// moveFile renames src to dir/name, adding " (2)", " (3)", ... before the
// extension if that name is taken, and falling back to an ASCII name if
// the file system refuses it. It returns the name used.
func moveFile(src, dir, name string) (string, error) {
	ext := filepath.Ext(name)
	bases := []string{strings.TrimSuffix(name, ext)}
	if a := asciiName(bases[0]); a != "" && a != bases[0] {
		bases = append(bases, a)
	}
	var lastErr error
	for _, base := range bases {
		for i := 1; i < 1000; i++ {
			n := base + ext
			if i > 1 {
				n = fmt.Sprintf("%s (%d)%s", base, i, ext)
			}
			if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
				continue
			}
			if err := os.Rename(src, filepath.Join(dir, n)); err != nil {
				lastErr = err
				break // try the next base name
			}
			return n, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("too many files with that name")
	}
	return "", lastErr
}

// SafeName makes s safe as a file or folder name (see FolderName); "" if
// nothing usable is left.
func SafeName(s string) string { return limit(sanitize(s)) }

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
		if _, err := os.Stat(filepath.Join(root, e.Name(), KeepMarker)); err == nil {
			continue
		}
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
	if n := limit(sanitize(discName)); n != "" {
		return n
	}
	label := strings.Join(strings.FieldsFunc(volumeLabel, func(r rune) bool { return r == '_' || r == '-' || unicode.IsSpace(r) }), " ")
	if n := limit(sanitize(titleCase(label))); n != "" {
		return n
	}
	return "Untitled disc"
}

// limit keeps names well under file systems' 255-byte limit, leaving room
// for " (2)" and ".mkv".
func limit(s string) string {
	const maxBytes = 200
	for len(s) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return strings.TrimRight(s, ". ")
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

// sanitize makes s safe as a single file or folder name on any file system
// blurip might write to, including exFAT/NTFS/FAT drives (common for
// external disks), which reject  < > : " / \ | ? *  and names ending in a
// dot or space. Like MakeMKV, "Title: Subtitle" becomes "Title - Subtitle".
func sanitize(s string) string {
	s = strings.ReplaceAll(s, ": ", " - ")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == ':' || r == '/' || r == '\\' || r == '|' || r == '<' || r == '>':
			return '-'
		case r == '"':
			return '\''
		case r == '?' || r == '*' || r == 0:
			return -1
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimLeft(s, ".")   // no hidden folders, no "." or ".."
	s = strings.TrimRight(s, ". ") // Windows file systems drop or reject these
	if isReservedWindowsName(s) {
		s += "_"
	}
	return s
}

// isReservedWindowsName reports device names that FAT/NTFS refuse.
func isReservedWindowsName(s string) bool {
	base, _, _ := strings.Cut(strings.ToUpper(s), ".")
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		return true
	}
	return false
}

// asciiName keeps only letters, digits, spaces and . _ - ( ) from name.
func asciiName(name string) string {
	s := strings.Map(func(r rune) rune {
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(" ._-()", r)):
			return r
		}
		return -1
	}, name)
	return sanitize(s)
}
