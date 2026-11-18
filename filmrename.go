package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/rip"
	"github.com/computerguymackay/blurip/internal/store"
)

// `blurip rename [NAME]` renames a film that has been ripped: its rip
// folder and the files named after it, its encoded folder and files
// (subtitles too), its encode queue entries, its rip folder in the
// history, and the name blurip remembers for the disc. (Box sets are
// renamed with blurip boxset fix.)

// filmRename is how a film is to be renamed.
type filmRename struct {
	job              store.Job
	oldName, newName string
	files            [][2]string // file renames, in their folders as they are now
	folders          [][2]string // then folder renames
	encodes          []store.Encode
	outputs          map[int64]string
	ripDir           string            // the rip folder afterwards
	renamed          map[string]string // file renames, by the file's path now
}

// named reports whether a name is the film's ("Old", "Old.mkv",
// "Old.en.srt", "Old - title02.mkv") and returns the rest of it.
func (r *filmRename) named(base string) (rest string, ok bool) {
	if rest, ok := strings.CutPrefix(base, r.oldName); ok && (rest == "" || strings.HasPrefix(rest, ".") || strings.HasPrefix(rest, " ")) {
		return rest, true
	}
	return "", false
}

// folder is where the folder dir ends up: renamed if it is named after
// the film, otherwise where it is.
func (r *filmRename) folder(dir string) string {
	if filepath.Base(dir) == r.oldName {
		return filepath.Join(filepath.Dir(dir), r.newName)
	}
	return dir
}

// moved is where the file at path ends up.
func (r *filmRename) moved(path string) string {
	base := filepath.Base(path)
	if dst, ok := r.renamed[path]; ok {
		base = filepath.Base(dst)
	}
	return filepath.Join(r.folder(filepath.Dir(path)), base)
}

func (s *session) planFilmRename(ctx context.Context, job store.Job, newName string) (*filmRename, error) {
	r := &filmRename{job: job, oldName: filepath.Base(job.OutputPath), newName: newName,
		outputs: map[int64]string{}, renamed: map[string]string{}}
	r.ripDir = r.folder(job.OutputPath)

	// Every folder of the film (the rip's, the encode's): its files named
	// after the film are renamed, then the folder itself if it is. In a
	// folder not named after the film (shared with other films), only
	// "Old.*" is its: "Old 2.mp4" beside it is another film.
	done := map[string]bool{}
	addFolder := func(dir string) error {
		if done[dir] {
			return nil
		}
		done[dir] = true
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil // deleted: nothing to rename
		} else if err != nil {
			return err
		}
		own := filepath.Base(dir) == r.oldName
		for _, e := range entries {
			if rest, ok := r.named(e.Name()); ok && !e.IsDir() && (own || strings.HasPrefix(rest, ".")) {
				dst := filepath.Join(dir, r.newName+rest)
				if _, err := os.Lstat(dst); err == nil {
					return fmt.Errorf("%s is in the way", dst)
				}
				r.files = append(r.files, [2]string{filepath.Join(dir, e.Name()), dst})
				r.renamed[filepath.Join(dir, e.Name())] = dst
			}
		}
		if nd := r.folder(dir); nd != dir {
			if _, err := os.Lstat(nd); err == nil {
				return fmt.Errorf("%s is in the way", nd)
			}
			r.folders = append(r.folders, [2]string{dir, nd})
		}
		return nil
	}
	if err := addFolder(job.OutputPath); err != nil {
		return nil, err
	}

	// Its queue entries: those from this rip, and any added by path.
	encodes, err := s.store.EncodesForJob(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	for _, f := range mkvFilesIn(job.OutputPath) {
		es, err := s.store.EncodesForSource(ctx, f)
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			if !slices.ContainsFunc(encodes, func(x store.Encode) bool { return x.ID == e.ID }) {
				encodes = append(encodes, e)
			}
		}
	}
	for _, e := range encodes {
		if e.Status == store.EncodeEncoding {
			return nil, fmt.Errorf("%s is being encoded; try again when it has finished", e.Title)
		}
		if filepath.Dir(e.SourcePath) == job.OutputPath {
			e.SourcePath = r.moved(e.SourcePath)
		}
		if rest, ok := r.named(e.Title); ok {
			e.Title = r.newName + rest
		}
		destDir, destBase := filepath.Split(e.DestName)
		if filepath.Base(filepath.Clean(destDir)) == r.oldName {
			destDir = filepath.Join(filepath.Dir(filepath.Clean(destDir)), r.newName)
		}
		if rest, ok := r.named(destBase); ok {
			destBase = r.newName + rest
		}
		e.DestName = filepath.Join(destDir, destBase)
		if e.OutputPath != "" {
			if _, err := os.Stat(e.OutputPath); err == nil {
				if err := addFolder(filepath.Dir(e.OutputPath)); err != nil {
					return nil, err
				}
			}
			r.outputs[e.ID] = r.moved(e.OutputPath)
		}
		r.encodes = append(r.encodes, e)
	}
	return r, nil
}

func (r *filmRename) print(out io.Writer) {
	fmt.Fprintf(out, "\nThe rename: %s → %s\n", r.oldName, r.newName)
	for _, f := range r.files {
		fmt.Fprintf(out, "    %s → %s\n", f[0], filepath.Base(f[1]))
	}
	for _, f := range r.folders {
		fmt.Fprintf(out, "    %s → %s\n", f[0], filepath.Base(f[1]))
	}
	fmt.Fprintf(out, "    %d encode queue entries updated; the disc is remembered as %q\n\n", len(r.encodes), r.newName)
}

// apply renames the files, then the folders (undoing it all if one
// fails), then updates the records.
func (s *session) applyFilmRename(ctx context.Context, r *filmRename) error {
	all := append(slices.Clone(r.files), r.folders...)
	for i, m := range all {
		if err := os.Rename(m[0], m[1]); err != nil {
			for j := i - 1; j >= 0; j-- {
				os.Rename(all[j][1], all[j][0])
			}
			return fmt.Errorf("renaming %s: %w (nothing was changed)", m[0], err)
		}
	}
	for _, e := range r.encodes {
		if err := s.store.UpdateEncode(ctx, e); err != nil {
			return err
		}
		if out, ok := r.outputs[e.ID]; ok {
			if err := s.store.SetEncodeOutput(ctx, e.ID, out); err != nil {
				return err
			}
		}
	}
	if err := s.store.MoveDiscRips(ctx, r.job.DiscID, r.job.OutputPath, r.ripDir); err != nil {
		return err
	}
	return s.store.SetDiscName(ctx, r.job.DiscID, r.newName)
}

func runRename(ctx context.Context, dbPath string, args []string) error {
	return filmRenameCmd(ctx, dbPath, args, os.Stdin, os.Stdout)
}

func filmRenameCmd(ctx context.Context, dbPath string, args []string, in io.Reader, out io.Writer) error {
	if len(args) > 1 {
		return errors.New(`usage: blurip rename [NAME]   (quote a name with spaces: blurip rename "Wild Card")`)
	}
	lock, err := store.AcquireLock(dbPath)
	if err != nil {
		return fmt.Errorf("%w (stop blurip before renaming a film)", err)
	}
	defer lock.Release()
	st, cfg, err := openStore(ctx, dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ask := prompt.NewPrompter(in, out).WithContext(ctx)
	s := &session{cfg: cfg, store: st, ask: ask, out: out, now: time.Now}

	rips, err := st.FilmRips(ctx)
	if err != nil {
		return err
	}
	if len(args) == 1 {
		want := strings.ToLower(args[0])
		rips = slices.DeleteFunc(rips, func(j store.Job) bool {
			return !strings.Contains(strings.ToLower(filepath.Base(j.OutputPath)), want) &&
				!strings.Contains(strings.ToLower(j.DiscName), want)
		})
	}
	var job store.Job
	switch {
	case len(rips) == 0 && len(args) == 1:
		return fmt.Errorf("no ripped film matches %q (box sets are renamed with blurip boxset fix)", args[0])
	case len(rips) == 0:
		return errors.New("no films have been ripped yet")
	case len(rips) == 1:
		job = rips[0]
	default:
		const most = 25
		var opts []prompt.Option
		for i, j := range rips[:min(len(rips), most)] {
			opts = append(opts, prompt.Option{Value: fmt.Sprint(i),
				Label: fmt.Sprintf("%s (ripped %s)", filepath.Base(j.OutputPath), j.StartedAt.Local().Format("2006-01-02"))})
		}
		if len(rips) > most {
			fmt.Fprintf(out, "The %d most recent of %d films (give part of a name to find others: blurip rename NAME).\n", most, len(rips))
		}
		pick, err := ask.Choice("Which film?", opts, "0")
		if err != nil {
			return err
		}
		var i int
		fmt.Sscan(pick, &i)
		job = rips[i]
	}

	old := filepath.Base(job.OutputPath)
	fmt.Fprintf(out, "\n%s: ripped %s to %s\n", old, job.StartedAt.Local().Format("2006-01-02 15:04"), job.OutputPath)
	ans, err := ask.String("New name", old)
	if err != nil {
		return err
	}
	name := rip.SafeName(ans)
	switch {
	case name == "":
		return fmt.Errorf("%q can't be used as a name (it's used for folders)", ans)
	case name == old:
		fmt.Fprintln(out, "That's its name already: nothing to change.")
		return nil
	}
	r, err := s.planFilmRename(ctx, job, name)
	if err != nil {
		return fmt.Errorf("can't rename it: %w", err)
	}
	r.print(out)
	if ok, err := ask.YesNo("Go ahead?", false); err != nil || !ok {
		if err == nil {
			fmt.Fprintln(out, "Nothing was changed.")
		}
		return err
	}
	if err := s.applyFilmRename(context.WithoutCancel(ctx), r); err != nil {
		return err
	}
	fmt.Fprintf(out, "✓ %s renamed %s (%d files, %d folders)\n", old, name, len(r.files), len(r.folders))
	return nil
}
