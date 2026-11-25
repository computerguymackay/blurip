package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/computerguymackay/blurip/internal/awake"
	"github.com/computerguymackay/blurip/internal/boxset"
	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/handbrake"
	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/space"
	"github.com/computerguymackay/blurip/internal/store"
	"github.com/dustin/go-humanize"
)

// encodeTempDirName is the folder inside a destination folder where
// encodes in progress are written, so a half-written file never appears
// in the library. (Not rip.TempDirName: the ripper cleans that one up.)
const encodeTempDirName = ".blurip-encode-tmp"

// encoder works through the encode queue (blurip encode).
type encoder struct {
	cfg     config.Config
	store   *store.Store
	out     io.Writer
	tty     bool
	now     func() time.Time
	presets []handbrake.Category

	// wait: when the queue is empty, wait for more (blurip -Q in another
	// terminal) instead of stopping.
	wait bool
	poll time.Duration // how often to look for new entries while waiting
	// keys pressed (q: stop after the current encode); nil = none.
	keys <-chan byte

	// awake keeps the PC from sleeping while encoding (nil: not).
	awake *awake.Inhibitor
	// dirs checks the rips, films and TV folders are still there (nil:
	// the configured ones; see dirWatch).
	dirs *dirWatch

	encode   func(context.Context, handbrake.Request) error
	duration func(ctx context.Context, bin, file string) (time.Duration, error)
	scan     func(ctx context.Context, bin, file string) (handbrake.ScanResult, error)
}

func newEncoder(cfg config.Config, s *store.Store, out io.Writer, tty bool, presets []handbrake.Category) *encoder {
	return &encoder{cfg: cfg, store: s, out: out, tty: tty, now: time.Now, presets: presets,
		poll: 5 * time.Second, encode: handbrake.Encode, duration: handbrake.Duration, scan: handbrake.Scan,
		awake: awake.New("Encoding with HandBrake", nil)}
}

// errNoRoom means an entry's destination disk hasn't room for its encode
// (see estimateEncode): the entry stays in the queue and the encoder waits
// for space to be freed.
var errNoRoom = errors.New("no room")

// noRoomError is errNoRoom with the reason.
type noRoomError struct{ msg string }

func (e noRoomError) Error() string        { return e.msg }
func (e noRoomError) Is(target error) bool { return target == errNoRoom }

// spaceWait is how often a destination disk too full to encode to is
// checked again.
var spaceWait = 30 * time.Second

// errStopEncoding means the encoder can't carry on (a destination folder
// is missing); the entry stays in the queue.
var errStopEncoding = errors.New("stopped")

// run encodes until the queue is empty (or, with wait, until q or
// Ctrl-C). It returns errInterrupted if an encode was stopped with Ctrl-C.
func (e *encoder) run(ctx context.Context) error {
	b := newBoard(e.out, e.tty)
	defer b.Close()
	row := b.Row("handbrake", "HandBrake")
	row.SetIdle("idle")
	if e.awake != nil {
		e.awake.OnLost = func(err error) {
			b.Logf("⚠ couldn't keep the PC awake, so it may go to sleep mid-encode: %v", err)
		}
	}
	var stopAfter atomic.Bool
	var busy atomic.Bool
	setHeader := func() {
		n := 0
		if es, err := e.store.Encodes(context.WithoutCancel(ctx), store.EncodeWaiting); err == nil {
			n = len(es)
		}
		keys := "Ctrl-C to stop"
		if e.keys != nil {
			keys = "q: stop after this one · Ctrl-C: stop now"
			if stopAfter.Load() {
				keys = "stopping after this one (q to carry on) · Ctrl-C: stop now"
			}
		}
		b.SetHeader(fmt.Sprintf("blurip encode · %d waiting · %s", n, keys))
	}
	wake := make(chan struct{}, 1)
	if e.keys != nil {
		go func() {
			for k := range e.keys {
				if k != 'q' && k != 'Q' {
					continue
				}
				now := !stopAfter.Load()
				stopAfter.Store(now)
				switch {
				case now && busy.Load():
					b.Logf("Stopping after this encode; press q again to carry on.")
				case !now:
					b.Logf("Carrying on with the queue.")
				}
				setHeader()
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		}()
	}

	if e.dirs == nil {
		e.dirs = newDirWatch(e.cfg.OutputDir, e.cfg.MoviesDir, e.cfg.ShowsDir)
	}
	// A folder gone (a network mount dropped while the PC slept): stop
	// rather than encode to the empty folder left behind, or hang.
	dirsGone := func() error {
		problems := e.dirs.check()
		if len(problems) == 0 {
			return nil
		}
		for _, p := range problems {
			b.Logf("✗ %s", p)
		}
		b.Logf("Stopped: mount the folder again and run blurip encode to carry on (the queue is kept).")
		return fmt.Errorf("%w: %s", errStopEncoding, problems[0])
	}
	if err := dirsGone(); err != nil {
		return err
	}

	var done, failed int
	var noRoom int64 // the entry last said to be waiting for space
	defer func() {
		if done+failed > 0 {
			b.Logf("Encoded %d, failed %d.", done, failed)
		}
	}()
	for {
		setHeader()
		if ctx.Err() != nil {
			return nil
		}
		if stopAfter.Load() {
			if es, _ := e.store.Encodes(context.WithoutCancel(ctx), store.EncodeWaiting); len(es) > 0 {
				b.Logf("Stopped; %d left in the queue. Run `blurip encode` to carry on.", len(es))
			}
			return nil
		}
		next, ok, err := e.store.NextEncode(ctx)
		if err != nil {
			return err
		}
		if !ok {
			if !e.wait {
				b.Logf("The encode queue is empty.")
				return nil
			}
			row.Activity("Waiting for rips to be queued (blurip -Q)")
			select {
			case <-ctx.Done():
				return nil
			case <-wake:
			case <-time.After(e.poll):
			}
			if time.Since(e.dirs.last) >= dirCheckEvery {
				if err := dirsGone(); err != nil {
					return err
				}
			}
			continue
		}
		if err := dirsGone(); err != nil {
			return err
		}
		busy.Store(true)
		release := e.awake.Hold()
		ok, err = e.encodeOne(ctx, row, next)
		release()
		busy.Store(false)
		if errors.Is(err, errNoRoom) {
			if noRoom != next.ID {
				noRoom = next.ID
				b.Logf("⏸ %s: %v.\n          Waiting: free some space on %s and it carries on by itself (q or Ctrl-C to stop).",
					next.Title, err, next.DestDir)
			}
			row.SetDisc(next.Title, "")
			row.Activity("Waiting for space on " + next.DestDir)
			select {
			case <-ctx.Done():
				return nil
			case <-wake:
			case <-time.After(spaceWait):
			}
			continue
		}
		noRoom = 0
		row.Idle()
		switch {
		case err != nil:
			return err
		case ok:
			done++
		default:
			failed++
		}
	}
}

// encodeOne encodes an entry: into a temp folder, checked, then moved
// into place, and the rip deleted if so set. ok reports success; a failed
// encode is recorded (err nil) and the rip kept.
func (e *encoder) encodeOne(ctx context.Context, row *driveRow, en store.Encode) (ok bool, err error) {
	dbctx := context.WithoutCancel(ctx)
	if fi, err := os.Stat(en.DestDir); err != nil || !fi.IsDir() {
		row.Log("✗ the folder %s is missing (is its drive mounted?); %s stays in the queue", en.DestDir, en.Title)
		return false, fmt.Errorf("%w: %s is missing", errStopEncoding, en.DestDir)
	}
	fail := func(reason string) (bool, error) {
		en.Status, en.FailureReason, en.FinishedAt = store.EncodeFailed, reason, e.now()
		if err := e.store.FinishEncode(dbctx, en); err != nil {
			return false, err
		}
		row.Log("✗ %s: %s (the rip was kept)", en.Title, reason)
		return false, nil
	}
	src, err := os.Stat(en.SourcePath)
	if err != nil {
		return fail(fmt.Sprintf("the rip %s is missing", en.SourcePath))
	}
	en.BytesIn = src.Size()
	// One episode of a title holding several: only its chapters are
	// encoded, so it is checked against their length, and counts as its
	// share of the rip's size.
	var partLen time.Duration
	if en.Chapters != "" {
		sc, err := e.scan(ctx, e.cfg.HandBrakeCLI, en.SourcePath)
		if ctx.Err() != nil {
			return false, errInterrupted
		}
		if err != nil {
			return fail(fmt.Sprintf("couldn't read the rip's chapters: %v", err))
		}
		if len(sc.Chapters) == 0 {
			return fail("HandBrake's scan of the rip listed no chapters, so its episode can't be picked out")
		}
		cs, err := boxset.ParseRange(en.Chapters)
		if err != nil {
			return fail(err.Error())
		}
		if partLen, err = handbrake.ChapterLength(sc.Chapters, cs.First, cs.Last); err != nil {
			return fail(err.Error())
		}
		if sc.Duration > 0 {
			en.BytesIn = int64(float64(src.Size()) * min(1, float64(partLen)/float64(sc.Duration)))
		}
	}
	preset, found := handbrake.FindPreset(e.presets, en.Preset)
	if !found {
		return fail(fmt.Sprintf("HandBrake has no preset %q; choose another with `blurip encode -U`", en.Preset))
	}
	format := foreignFormat(en, preset.Format)

	// Room for it? Its size is estimated from earlier encodes.
	if err := e.checkRoom(dbctx, en, en.BytesIn); err != nil {
		return false, err
	}

	tmpDir := filepath.Join(en.DestDir, encodeTempDirName, fmt.Sprintf("encode-%d", en.ID))
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return fail(fmt.Sprintf("creating a temp folder: %v", err))
	}
	defer func() {
		os.RemoveAll(tmpDir)
		os.Remove(filepath.Dir(tmpDir)) // only if empty: no other encode in it
	}()
	tmp := filepath.Join(tmpDir, filepath.Base(en.DestName)+"."+format)
	final := filepath.Join(en.DestDir, en.DestName+"."+format)

	row.SetDisc(en.Title, final)
	row.Activity(fmt.Sprintf("%s / %s, %s", preset.Category, preset.Name, tracksLabel(en)))
	if err := e.store.StartEncode(dbctx, en.ID, e.now()); err != nil {
		return false, err
	}
	start := e.now()
	err = e.encode(ctx, handbrake.Request{
		Bin: e.cfg.HandBrakeCLI, Input: en.SourcePath, Output: tmp, Preset: preset.Ref(), Format: format, AllTracks: en.AllTracks,
		Chapters: en.Chapters,
		OnProgress: func(p handbrake.Progress) {
			const max = 1_000_000
			row.Progress(makemkv.Progress{Task: p.Task, Total: int(p.Fraction * max), Max: max})
		},
	})
	row.ProgressDone()
	interrupted := func() (bool, error) {
		if err := e.store.RequeueEncode(dbctx, en.ID); err != nil {
			return false, err
		}
		row.Log("%s: stopped; it is still in the queue and starts again from the beginning next time", en.Title)
		return false, errInterrupted
	}
	if ctx.Err() != nil {
		return interrupted()
	}
	if err != nil {
		return fail(err.Error())
	}

	row.Activity("Checking the encode...")
	out, err := os.Stat(tmp)
	if err != nil || out.Size() == 0 {
		return fail("HandBrake finished but produced no file")
	}
	srcLen := partLen
	if en.Chapters == "" {
		srcLen, err = e.duration(ctx, e.cfg.HandBrakeCLI, en.SourcePath)
		if ctx.Err() != nil {
			return interrupted()
		}
		if err != nil {
			return fail(fmt.Sprintf("couldn't check the rip's length: %v", err))
		}
	}
	outLen, err := e.duration(ctx, e.cfg.HandBrakeCLI, tmp)
	if ctx.Err() != nil {
		return interrupted()
	}
	if err != nil {
		return fail(fmt.Sprintf("couldn't check the encode's length: %v", err))
	}
	if err := handbrake.CheckLength(srcLen, outLen); err != nil {
		return fail(err.Error())
	}

	// Into place. An encode saved as MKV in the rip's own folder would
	// have the rip's name: replace the rip only if it is to be deleted.
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return fail(fmt.Sprintf("creating %s: %v", filepath.Dir(final), err))
	}
	// The rip is deleted if so set, unless another entry is still to
	// encode it (another preset being tried, say).
	deleteRip := en.DeletesRip(e.cfg.DeleteAfterEncode)
	needed := false
	if deleteRip {
		if needed, err = e.store.SourceStillQueued(dbctx, en.SourcePath, en.ID); err != nil {
			return false, err
		}
		deleteRip = !needed
	}
	replacing := final == en.SourcePath && deleteRip
	// An earlier encode of the same rip (a box set disc ripped again) is
	// replaced, not kept beside it as "(2)".
	again, err := e.store.EncodedFrom(dbctx, final, en.SourcePath, en.Preset, en.ID)
	if err != nil {
		return false, err
	}
	if !replacing && !again {
		final = freeName(final)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fail(fmt.Sprintf("moving the encode into place: %v", err))
	}
	en.Status, en.OutputPath, en.BytesOut, en.FinishedAt = store.EncodeDone, final, out.Size(), e.now()
	en.Length = srcLen
	if deleteRip {
		if !replacing {
			if err := os.Remove(en.SourcePath); err != nil {
				row.Log("⚠ couldn't delete the rip %s: %v", en.SourcePath, err)
			} else {
				removeEmptyDirs(filepath.Dir(en.SourcePath), e.cfg.OutputDir)
			}
		}
		en.SourceDeleted = true
	}
	if err := e.store.FinishEncode(dbctx, en); err != nil {
		return false, err
	}
	deleted := ""
	switch {
	case en.SourceDeleted:
		deleted = "; rip deleted"
	case needed:
		deleted = "; rip kept: it is queued again"
	}
	row.Log("✓ %s → %s (%s → %s in %s%s)", en.Title, final, humanize.IBytes(uint64(en.BytesIn)),
		humanize.IBytes(uint64(en.BytesOut)), formatDuration(e.now().Sub(start)), deleted)
	return true, nil
}

// checkRoom returns an error wrapping errNoRoom (and the
// *space.NoRoomError) if the encode, its size estimated from earlier
// encodes, would fill its destination disk past encode_max_fill_percent.
func (e *encoder) checkRoom(ctx context.Context, en store.Encode, srcBytes int64) error {
	done, err := e.store.Encodes(ctx, store.EncodeDone)
	if err != nil {
		return err
	}
	need, basis := estimateEncode(done, en, srcBytes)
	u, err := space.Stat(en.DestDir)
	if err != nil {
		return err
	}
	if err := space.Check(u, need, 0, e.cfg.EncodeMaxFillPercent); err != nil {
		nr := err.(*space.NoRoomError)
		nr.What = "the encode"
		return noRoomError{fmt.Sprintf("%v (estimate: %s)", nr, basis)}
	}
	return nil
}

// foreignFormat is the format an encode is saved in. A foreign film from
// a Blu-ray (or an unknown disc) keeping every subtitle is saved as MKV,
// because MP4 can't hold Blu-ray (PGS) subtitles except burned in. A DVD's
// subtitles (VobSub) can go into MP4, so DVDs keep the preset's format.
func foreignFormat(e store.Encode, presetFormat string) string {
	if e.AllTracks && e.DiscKind != "DVD" {
		return "mkv"
	}
	return presetFormat
}

func tracksLabel(e store.Encode) string {
	switch {
	case e.AllTracks && e.DiscKind == "DVD":
		return "all audio and subtitle tracks"
	case e.AllTracks:
		return "all audio and subtitle tracks (MKV)"
	case !e.TracksAnswered:
		return "the preset's tracks (foreign film? not answered)"
	}
	return "the preset's tracks"
}

// freeName returns path, or "name (2).ext" and so on if it exists.
func freeName(path string) string {
	if _, err := os.Lstat(path); err != nil {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 2; ; i++ {
		p := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Lstat(p); err != nil {
			return p
		}
	}
}

// removeEmptyDirs removes dir and its parents while they are empty,
// stopping at root (which is kept). Nothing outside root is touched.
func removeEmptyDirs(dir, root string) {
	if root == "" {
		return
	}
	root = filepath.Clean(root)
	for dir = filepath.Clean(dir); dir != root; dir = filepath.Dir(dir) {
		rel, err := filepath.Rel(root, dir)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return
		}
		if os.Remove(dir) != nil {
			return // not empty
		}
	}
}

// cleanupEncodeTemp removes encodes left part-written by a stopped
// encoder. Only call it while holding the encode lock.
func cleanupEncodeTemp(dirs ...string) {
	for _, d := range dirs {
		if d != "" {
			os.RemoveAll(filepath.Join(d, encodeTempDirName))
		}
	}
}

// checkHandBrakeSandbox checks that a Flatpak HandBrake can use each
// folder (reading rips, writing encodes) and, with ask, offers to allow it.
func checkHandBrakeSandbox(ctx context.Context, bin string, dirs []string, out io.Writer, ask *prompt.Prompter) error {
	if makemkv.FlatpakAppOf(bin) == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, dir := range dirs {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		fmt.Fprintf(out, "Checking that HandBrake's Flatpak can use %s...\n", dir)
		err := makemkv.CheckSandboxFor(ctx, "HandBrake", bin, dir)
		var se *makemkv.SandboxError
		if !errors.As(err, &se) || ask == nil {
			if err != nil {
				return err
			}
			continue
		}
		fmt.Fprintf(out, "HandBrake runs as a Flatpak and isn't allowed to use %s (%s).\n", se.Dir, se.Why)
		yes, aerr := ask.YesNo(fmt.Sprintf("Allow it now? (runs: %s)", strings.Join(se.FixCommand(), " ")), true)
		if aerr != nil {
			return aerr
		}
		if !yes {
			return err
		}
		if err := makemkv.AllowFolder(ctx, se); err != nil {
			return err
		}
		if err := makemkv.CheckSandboxFor(ctx, "HandBrake", bin, dir); err != nil {
			return fmt.Errorf("access was given, but the check still fails: %w", err)
		}
		fmt.Fprintf(out, "Done: HandBrake can now use %s.\n", dir)
	}
	return nil
}
