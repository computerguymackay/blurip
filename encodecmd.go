package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/computerguymackay/blurip/internal/boxset"
	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/handbrake"
	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/setup"
	"github.com/computerguymackay/blurip/internal/store"
)

const encodeUsage = `Usage: blurip encode [-U|--update-list] [-w|--wait]
       blurip encode -L|--list [-a|--all]
       blurip encode setup

Encodes the rips queued with ` + "`blurip -Q`" + ` with HandBrake, one at a time, into
your films and TV folders, then (if set up so) deletes each rip once its
encode has been checked. Press q to stop after the current encode, or
Ctrl-C to stop at once (that encode starts again next time).

  -U, --update-list  change the queue first: move entries, group them (DVDs,
                     Blu-rays or TV box sets together), change tracks, preset
                     or destination, add or remove entries
  -w, --wait         when the queue is empty, wait for more rips instead of
                     stopping (run blurip -Q in another terminal)
  -L, --list         show the queue and exit (-a, --all: include finished
                     and removed entries; also: blurip encode list)
  setup              choose HandBrake, the presets and the folders
`

func runEncode(ctx context.Context, dbPath string, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "setup":
			return runEncodeSetup(ctx, dbPath)
		case "list":
			fs := flag.NewFlagSet("encode list", flag.ContinueOnError)
			all := fs.Bool("a", false, "include finished and removed entries")
			if err := fs.Parse(args[1:]); err != nil {
				return err
			}
			return runEncodeList(ctx, dbPath, *all)
		case "help", "-h", "-help", "--help":
			fmt.Print(encodeUsage)
			return nil
		}
	}
	fs := flag.NewFlagSet("encode", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), encodeUsage) }
	var update, wait, list, all bool
	fs.BoolVar(&list, "list", false, "show the queue and exit")
	fs.BoolVar(&list, "L", false, "short for -list")
	fs.BoolVar(&all, "all", false, "with -L: include finished and removed entries")
	fs.BoolVar(&all, "a", false, "short for -all")
	fs.BoolVar(&update, "update-list", false, "change the queue first")
	fs.BoolVar(&update, "U", false, "short for -update-list")
	fs.BoolVar(&wait, "wait", false, "wait for more rips when the queue is empty")
	fs.BoolVar(&wait, "w", false, "short for -wait")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("unknown encode command %q", fs.Arg(0))
	}
	switch {
	case list && (update || wait):
		return errors.New("-L (--list) only shows the queue; use it on its own (or with -a)")
	case list:
		return runEncodeList(ctx, dbPath, all)
	case all:
		return errors.New("-a (--all) goes with -L (--list)")
	}

	s, cfg, err := openStore(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	term := isTerminal(os.Stdin)
	p := prompt.NewPrompter(os.Stdin, os.Stdout)
	if cfg, err = ensureEncodeSetup(ctx, s, cfg, p, term); err != nil {
		return err
	}

	var presets []handbrake.Category
	loadPresets := func() ([]handbrake.Category, error) {
		if presets == nil {
			cats, err := handbrake.ListPresets(ctx, cfg.HandBrakeCLI)
			if err != nil {
				return nil, err
			}
			presets = cats
		}
		return presets, nil
	}
	if update {
		if !term {
			return errors.New("-U asks questions, so it needs a terminal")
		}
		if err := editQueue(ctx, s, cfg, p, os.Stdout, loadPresets); err != nil {
			return err
		}
		waiting, err := s.Encodes(ctx, store.EncodeWaiting)
		if err != nil || len(waiting) == 0 {
			return err
		}
		if yes, err := p.YesNo(fmt.Sprintf("Start encoding now (%d waiting)?", len(waiting)), true); err != nil || !yes {
			return err
		}
	}

	lock, err := store.AcquireLock(dbPath + ".encode")
	if errors.Is(err, store.ErrLocked) {
		if update {
			fmt.Println("blurip encode is already running in another terminal; it will use your changes.")
			return nil
		}
		return errors.New("blurip encode is already running in another terminal")
	}
	if err != nil {
		return err
	}
	defer lock.Release()
	if n, err := s.RecoverEncodes(ctx); err != nil {
		return err
	} else if n > 0 {
		fmt.Printf("%d encode(s) didn't finish last time; they start again from the beginning.\n", n)
	}
	waiting, err := s.Encodes(ctx, store.EncodeWaiting)
	if err != nil {
		return err
	}
	dirs := []string{cfg.MoviesDir, cfg.ShowsDir}
	for _, e := range waiting {
		dirs = append(dirs, e.DestDir)
	}
	cleanupEncodeTemp(dirs...)
	if len(waiting) == 0 && !wait {
		fmt.Println("The encode queue is empty. Rip with `blurip -Q` to queue rips, or add them with `blurip encode -U`.")
		return nil
	}
	cats, err := loadPresets()
	if err != nil {
		return err
	}
	var ask *prompt.Prompter
	if term {
		ask = p
	}
	if err := checkHandBrakeSandbox(ctx, cfg.HandBrakeCLI, append(dirs, cfg.OutputDir), os.Stdout, ask); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	enc := newEncoder(cfg, s, os.Stdout, isTerminal(os.Stdout), cats)
	enc.wait = wait
	if term {
		keys, restore := readKeys(os.Stdin)
		defer restore()
		enc.keys = keys
	}
	err = enc.run(ctx)
	if errors.Is(err, errStopEncoding) {
		return errRipFailed // reason already shown
	}
	return err
}

// ensureEncodeSetup runs the encode setup if it hasn't been done.
func ensureEncodeSetup(ctx context.Context, s *store.Store, cfg config.Config, p *prompt.Prompter, term bool) (config.Config, error) {
	if cfg.EncodeReady() {
		return cfg, nil
	}
	if !term {
		return cfg, errors.New("HandBrake encoding isn't set up yet; run `blurip encode setup`")
	}
	fmt.Println("HandBrake encoding isn't set up yet.")
	fmt.Println()
	return encodeSetupAndSave(ctx, s, cfg, p)
}

func runEncodeSetup(ctx context.Context, dbPath string) error {
	s, cfg, err := openStore(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	p := prompt.NewPrompter(os.Stdin, os.Stdout)
	if cfg.EncodeReady() && isTerminal(os.Stdin) {
		return settingsMenu(ctx, s, cfg, p, true)
	}
	_, err = encodeSetupAndSave(ctx, s, cfg, p)
	return err
}

func encodeSetupAndSave(ctx context.Context, s *store.Store, cfg config.Config, p *prompt.Prompter) (config.Config, error) {
	next, err := setup.RunEncode(ctx, p, setup.DefaultEncodeEnv(), cfg)
	if err != nil {
		return cfg, err
	}
	if err := config.Save(ctx, s, next); err != nil {
		return cfg, err
	}
	fmt.Printf("\nEncoding settings saved to %s\n\n", s.Path())
	return next, nil
}

func runEncodeList(ctx context.Context, dbPath string, all bool) error {
	s, cfg, err := openStore(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	statuses := []string{store.EncodeEncoding, store.EncodeWaiting, store.EncodeFailed}
	if all {
		statuses = nil
	}
	es, err := s.Encodes(ctx, statuses...)
	if err != nil {
		return err
	}
	if len(es) == 0 {
		fmt.Println("The encode queue is empty.")
		return nil
	}
	printQueue(os.Stdout, cfg, es, true)
	return nil
}

// printQueue prints entries numbered from 1.
func printQueue(out io.Writer, cfg config.Config, es []store.Encode, details bool) {
	fmt.Fprintf(out, "  %-3s %-9s %-32s %-8s %-22s %-9s %s\n", "#", "STATUS", "TITLE", "FROM", "PRESET", "TRACKS", "TO")
	for i, e := range es {
		_, preset := handbrake.SplitRef(e.Preset)
		fmt.Fprintf(out, "  %-3d %-9s %-32s %-8s %-22s %-9s %s\n", i+1, e.Status, truncate(e.Title, 32), dash(e.DiscKind),
			truncate(preset, 22), tracksShort(e), destLabel(cfg, e.DestDir))
		if details {
			switch {
			case e.Status == store.EncodeFailed:
				fmt.Fprintf(out, "      %s\n", e.FailureReason)
			case e.Status == store.EncodeDone:
				fmt.Fprintf(out, "      → %s\n", e.OutputPath)
			}
		}
	}
}

func tracksShort(e store.Encode) string {
	switch {
	case e.AllTracks:
		return "Foreign"
	case !e.TracksAnswered:
		return "Standard?"
	}
	return "Standard"
}

func destLabel(cfg config.Config, dir string) string {
	switch dir {
	case cfg.MoviesDir:
		return "films (" + filepath.Base(dir) + ")"
	case cfg.ShowsDir:
		return "TV (" + filepath.Base(dir) + ")"
	}
	return dir
}

// editQueue is `blurip encode -U`: list the queue, change entries, add
// and remove them.
func editQueue(ctx context.Context, s *store.Store, cfg config.Config, p *prompt.Prompter, out io.Writer,
	presets func() ([]handbrake.Category, error)) error {
	for {
		es, err := s.Encodes(ctx, store.EncodeEncoding, store.EncodeWaiting, store.EncodeFailed)
		if err != nil {
			return err
		}
		fmt.Fprintln(out)
		if len(es) == 0 {
			fmt.Fprintln(out, "The encode queue is empty.")
		} else {
			fmt.Fprintf(out, "Encode queue: %d\n", len(es))
			printQueue(out, cfg, es, true)
		}
		fmt.Fprintln(out)
		ans, err := p.String("Number to change or move (3-5 or 1,4 for several), a to add, g to group, Enter to finish", "")
		if err != nil {
			return err
		}
		switch ans = strings.ToLower(strings.TrimSpace(ans)); ans {
		case "", "q":
			return nil
		case "a":
			if err := addToQueue(ctx, s, cfg, p, out); err != nil {
				return err
			}
			continue
		case "g":
			if len(es) > 1 {
				if err := groupEntries(ctx, s, p, es); err != nil {
					return err
				}
			}
			continue
		}
		nums, err := parseSelection(ans, len(es))
		if err != nil {
			fmt.Fprintf(out, "  %v\n", err)
			continue
		}
		var sel []store.Encode
		for _, n := range nums {
			if e := es[n-1]; e.Status == store.EncodeEncoding {
				fmt.Fprintf(out, "  %s is being encoded now, so it can't be changed.\n", e.Title)
			} else {
				sel = append(sel, e)
			}
		}
		if len(sel) > 0 {
			if err := editEntries(ctx, s, cfg, p, out, es, sel, presets); err != nil {
				return err
			}
		}
	}
}

// parseSelection reads "3", "3-5" or "1,4" (numbers from 1 to n).
func parseSelection(s string, n int) ([]int, error) {
	var out []int
	seen := map[int]bool{}
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		lo, hi, isRange := strings.Cut(part, "-")
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b := a
		var err2 error
		if isRange {
			b, err2 = strconv.Atoi(strings.TrimSpace(hi))
		}
		if err1 != nil || err2 != nil || a < 1 || b > n || a > b {
			return nil, fmt.Errorf("%q isn't a number from 1 to %d, or a range such as 2-4", part, n)
		}
		for i := a; i <= b; i++ {
			if !seen[i] {
				seen[i] = true
				out = append(out, i)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("give a number from 1 to %d", n)
	}
	return out, nil
}

// editEntries changes the entries sel, picked from the queue es.
func editEntries(ctx context.Context, s *store.Store, cfg config.Config, p *prompt.Prompter, out io.Writer, es, sel []store.Encode,
	presets func() ([]handbrake.Category, error)) error {
	first := sel[0]
	fmt.Fprintln(out)
	if len(sel) == 1 {
		fmt.Fprintln(out, first.Title)
	} else {
		fmt.Fprintf(out, "%d entries: %s ... %s\n", len(sel), first.Title, sel[len(sel)-1].Title)
	}
	_, presetName := handbrake.SplitRef(first.Preset)
	opts := []prompt.Option{
		{Value: "move", Label: "Move: change its place in the queue"},
		{Value: "tracks", Label: "Tracks: " + strings.ToLower(tracksLabel(first))},
		{Value: "preset", Label: "Preset: " + presetName},
		{Value: "dest", Label: "Destination: " + first.DestDir},
	}
	for _, e := range sel {
		if e.Status == store.EncodeFailed {
			opts = append(opts, prompt.Option{Value: "retry", Label: "Try again (put it back in the queue)"})
			break
		}
	}
	opts = append(opts,
		prompt.Option{Value: "remove", Label: "Remove from the queue (the rip is kept)"},
		prompt.Option{Value: "back", Label: "Back"})
	what, err := p.Choice("Change:", opts, "back")
	if err != nil {
		return err
	}
	change := func(f func(*store.Encode)) error {
		for _, e := range sel {
			f(&e)
			if err := s.UpdateEncode(ctx, e); err != nil {
				return err
			}
		}
		return nil
	}
	switch what {
	case "move":
		return moveInQueue(ctx, s, p, out, es, sel)
	case "tracks":
		def := "standard"
		if first.AllTracks {
			def = "all"
		}
		v, err := p.Choice("Keep which tracks?", []prompt.Option{
			{Value: "standard", Label: "The preset's usual tracks"},
			{Value: "all", Label: "Every audio and subtitle track (foreign film; Blu-rays are saved as MKV)"},
		}, def)
		if err != nil {
			return err
		}
		return change(func(e *store.Encode) { e.AllTracks, e.TracksAnswered = v == "all", true })
	case "preset":
		cats, err := presets()
		if err != nil {
			return err
		}
		ref, err := setup.ChoosePreset(p, cats, "Preset", first.Preset)
		if err != nil {
			return err
		}
		return change(func(e *store.Encode) { e.Preset = ref })
	case "dest":
		v, err := p.Choice("Encode into:", []prompt.Option{
			{Value: "movies", Label: "Films folder: " + cfg.MoviesDir},
			{Value: "shows", Label: "TV folder: " + cfg.ShowsDir},
			{Value: "other", Label: "Another folder"},
		}, map[string]string{cfg.MoviesDir: "movies", cfg.ShowsDir: "shows"}[first.DestDir])
		if err != nil {
			return err
		}
		dir := map[string]string{"movies": cfg.MoviesDir, "shows": cfg.ShowsDir}[v]
		if v == "other" {
			if dir, err = askExistingDir(p, out, "Folder"); err != nil {
				return err
			}
		}
		return change(func(e *store.Encode) { e.DestDir = dir })
	case "retry":
		return change(func(e *store.Encode) {
			if e.Status == store.EncodeFailed {
				e.Status, e.FailureReason = store.EncodeWaiting, ""
			}
		})
	case "remove":
		return change(func(e *store.Encode) { e.Status = store.EncodeRemoved })
	}
	return nil
}

func askExistingDir(p *prompt.Prompter, out io.Writer, question string) (string, error) {
	for {
		dir, err := p.String(question, "")
		if err != nil {
			return "", err
		}
		if dir = expandHome(strings.TrimSpace(dir)); dir == "" {
			continue
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() || !filepath.IsAbs(dir) {
			fmt.Fprintln(out, "  Please give the full path of a folder that exists.")
			continue
		}
		return filepath.Clean(dir), nil
	}
}

// addToQueue adds past rips that aren't queued, or files by path.
func addToQueue(ctx context.Context, s *store.Store, cfg config.Config, p *prompt.Prompter, out io.Writer) error {
	how, err := p.Choice("Add:", []prompt.Option{
		{Value: "rips", Label: "Past rips that aren't in the queue"},
		{Value: "path", Label: "An MKV file, or a folder of them, by path"},
		{Value: "back", Label: "Back"},
	}, "rips")
	if err != nil || how == "back" {
		return err
	}
	var add []store.Encode
	if how == "rips" {
		if add, err = chooseRips(ctx, s, cfg, p, out); err != nil {
			return err
		}
	} else {
		if add, err = choosePath(ctx, s, cfg, p, out); err != nil {
			return err
		}
	}
	if err := askForeign(ctx, s, p, out, add); err != nil {
		return err
	}
	for _, e := range add {
		if _, err := s.AddEncode(ctx, e); err != nil {
			return err
		}
	}
	if len(add) > 0 {
		fmt.Fprintf(out, "Added %d.\n", len(add))
	}
	return nil
}

// askForeign asks, for entries being added, whether each film is foreign
// and each TV show in a foreign language (once per show, remembered as in
// box-set mode), and sets their tracks.
func askForeign(ctx context.Context, s *store.Store, p *prompt.Prompter, out io.Writer, add []store.Encode) error {
	shows := map[string]bool{}
	for i := range add {
		e := &add[i]
		if m := episodeRE.FindStringSubmatch(e.Title); m != nil {
			show := m[1]
			all, known := shows[strings.ToLower(show)]
			if !known {
				var err error
				if all, known, err = s.ShowAllTracks(ctx, show); err != nil {
					return err
				}
				if known {
					fmt.Fprintf(out, "%s: %s (as answered before).\n", show, map[bool]string{true: "every audio and subtitle track", false: "the preset's tracks"}[all])
				} else if all, err = p.YesNo(fmt.Sprintf("Is %s a foreign-language show (keep every audio and subtitle track)?", show), false); err != nil {
					return err
				} else if err := s.SetShowAllTracks(ctx, show, all); err != nil {
					return err
				}
				shows[strings.ToLower(show)] = all
			}
			e.AllTracks, e.TracksAnswered = all, true
			continue
		}
		all, err := p.YesNo(fmt.Sprintf("Is %s a foreign film (keep every audio and subtitle track)?", e.Title), false)
		if err != nil {
			return err
		}
		e.AllTracks, e.TracksAnswered = all, true
	}
	return nil
}

type ripFile struct {
	path string
	kind string
	job  store.Job
}

func chooseRips(ctx context.Context, s *store.Store, cfg config.Config, p *prompt.Prompter, out io.Writer) ([]store.Encode, error) {
	jobs, err := s.History(ctx, store.HistoryFilter{})
	if err != nil {
		return nil, err
	}
	var files []ripFile
	seen := map[string]bool{}
	for _, j := range jobs {
		if j.Status != store.StatusSucceeded || j.OutputPath == "" {
			continue
		}
		for _, f := range mkvFilesIn(j.OutputPath) {
			if seen[f] {
				continue
			}
			seen[f] = true
			if q, err := s.IsQueued(ctx, f); err != nil {
				return nil, err
			} else if !q {
				files = append(files, ripFile{path: f, kind: makemkv.KindOf(j.DiscType), job: j})
			}
		}
	}
	if len(files) == 0 {
		fmt.Fprintln(out, "Every rip that is still on disk is already in the queue (or was encoded).")
		return nil, nil
	}
	const most = 40
	if len(files) > most {
		files = files[:most]
	}
	fmt.Fprintln(out, "Rips not in the queue (newest first):")
	for i, f := range files {
		fmt.Fprintf(out, "  %2d) %-8s %s  %s\n", i+1, dash(f.kind), f.job.StartedAt.Format("2006-01-02"), f.path)
	}
	ans, err := p.String("Which (e.g. 1,3 or 2-5; Enter for none)", "")
	if err != nil || strings.TrimSpace(ans) == "" {
		return nil, err
	}
	nums, err := parseSelection(ans, len(files))
	if err != nil {
		fmt.Fprintf(out, "  %v\n", err)
		return nil, nil
	}
	var add []store.Encode
	for _, n := range nums {
		f := files[n-1]
		e := queueEntryFor(cfg, f.path, f.kind)
		e.JobID, e.QueuedAt = f.job.ID, time.Now()
		add = append(add, e)
	}
	return add, nil
}

func choosePath(ctx context.Context, s *store.Store, cfg config.Config, p *prompt.Prompter, out io.Writer) ([]store.Encode, error) {
	path, err := p.String("Path of the MKV file or folder", "")
	if err != nil || strings.TrimSpace(path) == "" {
		return nil, err
	}
	path = filepath.Clean(expandHome(strings.TrimSpace(path)))
	fi, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(out, "  %v\n", err)
		return nil, nil
	}
	files := []string{path}
	if fi.IsDir() {
		if files = mkvFilesIn(path); len(files) == 0 {
			fmt.Fprintf(out, "  There are no MKV files in %s.\n", path)
			return nil, nil
		}
	}
	kind, err := p.Choice("Which preset should they use?", []prompt.Option{
		{Value: "DVD", Label: "DVD preset: " + presetName(cfg.PresetDVD)},
		{Value: "Blu-ray", Label: "Blu-ray preset: " + presetName(cfg.PresetBluRay)},
	}, "")
	if err != nil {
		return nil, err
	}
	var add []store.Encode
	for _, f := range files {
		if q, err := s.IsQueued(ctx, f); err != nil {
			return nil, err
		} else if q {
			fmt.Fprintf(out, "  %s is already in the queue.\n", f)
			continue
		}
		e := queueEntryFor(cfg, f, kind)
		e.QueuedAt = time.Now()
		add = append(add, e)
	}
	return add, nil
}

func presetName(ref string) string {
	_, n := handbrake.SplitRef(ref)
	return n
}

// mkvFilesIn returns the MKV files in dir (or dir itself, if it is one),
// sorted.
func mkvFilesIn(dir string) []string {
	if strings.EqualFold(filepath.Ext(dir), ".mkv") {
		if _, err := os.Stat(dir); err == nil {
			return []string{dir}
		}
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".mkv") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// episodeRE matches blurip's box-set file names: "Killjoys - S01E02".
var episodeRE = regexp.MustCompile(`^(.+) - S(\d{1,3})E\d{1,3}`)

// queueEntryFor works out where an MKV file's encode goes from its name:
// "Show - S01E02" goes to the TV folder as Show/Season 01/Show - S01E02,
// anything else to the films folder as Name/Name.
func queueEntryFor(cfg config.Config, src, kind string) store.Encode {
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	e := store.Encode{SourcePath: src, DiscKind: kind, Preset: cfg.Preset(kind), TracksAnswered: true}
	if m := episodeRE.FindStringSubmatch(base); m != nil {
		season, _ := strconv.Atoi(m[2])
		e.Title, e.DestDir, e.DestName = base, cfg.ShowsDir, filepath.Join(m[1], boxset.SeasonFolder(season), base)
		return e
	}
	dir := filepath.Dir(src)
	folder := filepath.Base(dir)
	ripRoot := dir == cfg.OutputDir || dir == cfg.DestDir("DVD") || dir == cfg.DestDir("Blu-ray")
	e.DestDir = cfg.MoviesDir
	if folder == base || ripRoot {
		e.Title, e.DestName = base, filepath.Join(base, base)
	} else {
		e.Title, e.DestName = folder+" - "+base, filepath.Join(folder, base)
	}
	return e
}

// moveInQueue asks where to move the entries sel (in their current order)
// in the queue es, and moves them.
func moveInQueue(ctx context.Context, s *store.Store, p *prompt.Prompter, out io.Writer, es, sel []store.Encode) error {
	where, err := p.Choice("Move to:", []prompt.Option{
		{Value: "top", Label: "The top (encoded next)"},
		{Value: "bottom", Label: "The bottom"},
		{Value: "before", Label: "Before another entry"},
	}, "top")
	if err != nil {
		return err
	}
	moving := map[int64]bool{}
	for _, e := range sel {
		moving[e.ID] = true
	}
	var before int64
	if where == "before" {
		for {
			n, err := p.Int("Before which entry (its number in the list)?", 1, 1, len(es))
			if err != nil {
				return err
			}
			if !moving[es[n-1].ID] {
				before = es[n-1].ID
				break
			}
			fmt.Fprintf(out, "  %d is one of the entries being moved; pick another.\n", n)
		}
	}
	return s.ReorderEncodes(ctx, moveEntries(es, moving, where, before))
}

// moveEntries returns the ids of the queue es with the entries in moving
// (keeping their order) moved to the "top" (after any being encoded now),
// the "bottom", or "before" the entry with id before.
func moveEntries(es []store.Encode, moving map[int64]bool, where string, before int64) []int64 {
	var rest, moved []store.Encode
	for _, e := range es {
		if moving[e.ID] {
			moved = append(moved, e)
		} else {
			rest = append(rest, e)
		}
	}
	at := len(rest)
	switch where {
	case "top":
		at = 0
		for at < len(rest) && rest[at].Status == store.EncodeEncoding {
			at++
		}
	case "before":
		for i, e := range rest {
			if e.ID == before {
				at = i
			}
		}
	}
	return encodeIDs(slices.Concat(rest[:at], moved, rest[at:]))
}

// groupEntries asks how to group the whole queue es, and does it.
func groupEntries(ctx context.Context, s *store.Store, p *prompt.Prompter, es []store.Encode) error {
	how, err := p.Choice("Put the queue in this order:", []prompt.Option{
		{Value: "dvd", Label: "DVDs first (they encode fastest), then Blu-rays"},
		{Value: "bluray", Label: "Blu-rays first, then DVDs"},
		{Value: "tv", Label: "TV box sets first, each show's episodes together, then films"},
		{Value: "films", Label: "Films first, then TV box sets (each show's episodes together)"},
		{Value: "added", Label: "The order they were added"},
		{Value: "back", Label: "Back (leave it as it is)"},
	}, "back")
	if err != nil || how == "back" {
		return err
	}
	return s.ReorderEncodes(ctx, encodeIDs(groupQueue(es, how)))
}

// groupQueue returns the queue es in the order how ("dvd", "bluray", "tv",
// "films" or "added"). Otherwise entries keep their order, and one being
// encoded now stays first.
func groupQueue(es []store.Encode, how string) []store.Encode {
	out := slices.Clone(es)
	if how == "added" {
		slices.SortStableFunc(out, func(a, b store.Encode) int { return cmp.Compare(a.ID, b.ID) })
	}
	// Each show's episodes go where its first one is.
	firstOfShow := map[string]int{}
	for i, e := range out {
		if show := showOf(e); show != "" {
			if _, ok := firstOfShow[show]; !ok {
				firstOfShow[show] = i
			}
		}
	}
	rank := func(e store.Encode) int {
		if e.Status == store.EncodeEncoding {
			return -1
		}
		switch how {
		case "dvd":
			return map[string]int{"DVD": 0, "Blu-ray": 1}[e.DiscKind] + boolInt(e.DiscKind == "")*2
		case "bluray":
			return map[string]int{"Blu-ray": 0, "DVD": 1}[e.DiscKind] + boolInt(e.DiscKind == "")*2
		case "tv":
			return boolInt(showOf(e) == "")
		case "films":
			return boolInt(showOf(e) != "")
		}
		return 0
	}
	place := func(i int, e store.Encode) int {
		if (how == "tv" || how == "films") && showOf(e) != "" {
			return firstOfShow[showOf(e)]
		}
		return i
	}
	idx := make(map[int64]int, len(out))
	for i, e := range out {
		idx[e.ID] = place(i, e)
	}
	slices.SortStableFunc(out, func(a, b store.Encode) int {
		if c := cmp.Compare(rank(a), rank(b)); c != 0 {
			return c
		}
		return cmp.Compare(idx[a.ID], idx[b.ID])
	})
	return out
}

// showOf returns the TV show a box-set episode belongs to (its dest name
// is "Show/Season 01/Show - S01E01"), or "" for a film.
func showOf(e store.Encode) string {
	parts := strings.Split(filepath.ToSlash(e.DestName), "/")
	if len(parts) < 3 {
		return ""
	}
	return parts[0]
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func encodeIDs(es []store.Encode) []int64 {
	ids := make([]int64, len(es))
	for i, e := range es {
		ids[i] = e.ID
	}
	return ids
}
