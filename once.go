package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/computerguymackay/blurip/internal/config"
	"github.com/computerguymackay/blurip/internal/drive"
	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/rip"
	"github.com/computerguymackay/blurip/internal/space"
	"github.com/computerguymackay/blurip/internal/store"
	"github.com/dustin/go-humanize"
)

// errInterrupted makes main exit with status 130 without printing an error.
var errInterrupted = errors.New("interrupted")

// errRipFailed means a rip failed; the reason was already printed and recorded.
var errRipFailed = errors.New("rip failed")

// Track selection recorded in job_titles until blurip chooses tracks itself.
const selectionMakeMKVDefault = "makemkv-default"

// session is everything needed to handle discs.
type session struct {
	cfg   config.Config
	store *store.Store
	ctl   drive.Controller
	ask   *prompt.Prompter
	out   io.Writer // for questions only (the panel is hidden meanwhile)
	view  *driveRow // this drive's entry on the board
	now   func() time.Time

	// reservations is shared by concurrent rips so their space checks see
	// each other. nil = private to this session.
	reservations *space.Reservations
	// askLock, if set, is called before asking the user anything and returns
	// the function that ends the questions. Parallel mode uses it so only
	// one drive asks at a time.
	askLock func() (release func())
	// infoTimeout limits reading the disc; 0 = defaultInfoTimeout.
	infoTimeout time.Duration

	// Recorded with each job: the drive mode in effect (single, sequential,
	// parallel), the drives being used, and the rip counter shared by all
	// drives (nil = this session's rips are the only ones).
	mode       config.DriveMode
	drivesUsed string
	rips       *ripCounter

	// boxSet, if set, rips every disc as part of a TV box set (--box-set).
	boxSet *boxSetState

	// canAsk: questions can be answered (stdin is a terminal). Without it,
	// a disc whose name looks like a code keeps that name.
	canAsk bool
	// pending counts questions still open after their disc was handed on
	// (a disc's name, asked while it rips); nil = not tracked.
	pending *sync.WaitGroup

	// queue (-Q): add each finished rip to the HandBrake encode queue.
	queue bool
}

// ripCounter tracks how many rips run at once, so each job can record the
// most that ran at the same time as it.
type ripCounter struct {
	mu     sync.Mutex
	nextID int
	peaks  map[int]int // running rip → most rips seen at once during it
}

// start registers a rip and returns its ID for finish.
func (c *ripCounter) start() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.peaks == nil {
		c.peaks = map[int]int{}
	}
	c.nextID++
	c.peaks[c.nextID] = 0
	n := len(c.peaks)
	for id, p := range c.peaks {
		c.peaks[id] = max(p, n)
	}
	return c.nextID
}

// finish ends a rip and returns the most rips that ran at once during it.
func (c *ripCounter) finish(id int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.peaks[id]
	delete(c.peaks, id)
	return p
}

// openForRipping takes the single-instance lock, opens the database and
// recovers from any previous crash. The caller must call cleanup.
func openForRipping(ctx context.Context, dbPath string, out io.Writer) (s *store.Store, cfg config.Config, cleanup func(), err error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, cfg, nil, err
	}
	lock, err := store.AcquireLock(dbPath)
	if err != nil {
		return nil, cfg, nil, err
	}
	s, cfg, err = openStore(ctx, dbPath)
	if err != nil {
		lock.Release()
		return nil, cfg, nil, err
	}
	cleanup = func() { s.Close(); lock.Release() }
	if err := recoverJobs(ctx, s, cfg, out); err != nil {
		cleanup()
		return nil, cfg, nil, err
	}
	return s, cfg, cleanup, nil
}

// recoverJobs marks jobs left "running" by a killed blurip as interrupted
// and removes leftover temp folders. Only call it while holding the lock.
func recoverJobs(ctx context.Context, s *store.Store, cfg config.Config, out io.Writer) error {
	jobs, err := s.RecoverInterrupted(ctx)
	if err != nil {
		return fmt.Errorf("recovering unfinished jobs: %w", err)
	}
	for _, j := range jobs {
		fmt.Fprintf(out, "Job %d (%s, started %s) didn't finish last time; marked as interrupted.\n",
			j.ID, orUnknown(j.DiscName), j.StartedAt.Format("2006-01-02 15:04"))
	}
	if cfg.OutputDir != "" {
		if err := rip.CleanupTemp(cfg.OutputDir); err != nil {
			fmt.Fprintf(out, "Warning: couldn't remove old partial rips: %v\n", err)
		}
	}
	return nil
}

// runOnce rips the main feature of the first ready disc, then exits.
func runOnce(dbPath string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	s, cfg, cleanup, err := openForRipping(ctx, dbPath, os.Stdout)
	if err != nil {
		return err
	}
	defer cleanup()
	if !cfg.SetupComplete {
		return errors.New("blurip isn't set up yet; run `blurip setup` first")
	}
	devices, labels, err := enabledDrives(ctx, s)
	if err != nil {
		return err
	}
	// One prompter for everything: each reads stdin through a buffer.
	p := prompt.NewPrompter(os.Stdin, os.Stdout).WithContext(ctx)
	var sandboxAsk *prompt.Prompter
	if isTerminal(os.Stdin) {
		sandboxAsk = p
	}
	if err := checkSandbox(ctx, cfg, os.Stdout, sandboxAsk); err != nil {
		return err
	}

	ctl := drive.CDROM{UseEjectCommand: true}
	w := drive.NewWatcher(ctl, time.Second)
	w.OnError = func(dev string, err error) { fmt.Fprintf(os.Stderr, "%s: %v\n", dev, err) }
	ready := w.Poll(devices)
	if len(ready) == 0 {
		return fmt.Errorf("no disc ready in %s", strings.Join(devices, ", "))
	}
	b := newBoard(os.Stdout, isTerminal(os.Stdout))
	defer b.Close()
	sess := &session{
		cfg: cfg, store: s, ctl: ctl, ask: p, out: os.Stdout,
		view: b.Row(ready[0], labels[ready[0]]), now: time.Now,
		mode: config.DriveSingle, drivesUsed: labels[ready[0]],
		canAsk: isTerminal(os.Stdin), pending: &sync.WaitGroup{},
	}
	var askMu sync.Mutex
	sess.askLock = func() func() {
		askMu.Lock()
		b.Pause()
		return func() { b.Resume(); askMu.Unlock() }
	}
	err = sess.handleDisc(ctx, ready[0])
	sess.pending.Wait() // the disc's name, if it's still being asked
	return err
}

// enabledDrives returns the drives blurip should use, their devices, and
// each device's label ("Blu-ray drive (sr1)").
func enabledDrives(ctx context.Context, s *store.Store) (devices []string, labels map[string]string, err error) {
	drives, err := s.Drives(ctx)
	if err != nil {
		return nil, nil, err
	}
	labels = map[string]string{}
	for _, d := range drives {
		if d.Enabled {
			devices = append(devices, d.Device)
			labels[d.Device] = d.Label()
		}
	}
	if len(devices) == 0 {
		return nil, nil, errors.New("no drives enabled; run `blurip setup`")
	}
	return devices, labels, nil
}

// handleDisc identifies the disc in dev, skips it if it was already ripped
// (unless the user asks to rip it again), otherwise rips the main feature.
// Every outcome is recorded as a job.
func (s *session) handleDisc(ctx context.Context, dev string) error {
	err := s.handle(ctx, dev)
	if err != nil && ctx.Err() != nil && !errors.Is(err, errInterrupted) {
		return errInterrupted // e.g. Ctrl-C while a question was waiting
	}
	return err
}

// readRetryDelay is how long to wait before reading a disc again that
// MakeMKV couldn't open.
var readRetryDelay = 15 * time.Second

// readTimeout is how long MakeMKV may take to read a disc, so one bad disc
// can't hold up the other drives; 0 means no limit.
func (s *session) readTimeout() time.Duration {
	if s.infoTimeout != 0 {
		return s.infoTimeout
	}
	return time.Duration(s.cfg.ReadTimeout) * time.Minute
}

// plannedRip is a disc that has been read and decided on, ready to rip.
type plannedRip struct {
	dev     string
	job     store.Job // filled in so far: disc, drive, mode, start and read time
	choice  ripChoice
	kind    string      // "DVD", "Blu-ray" or ""
	readyAt time.Time   // when it started waiting for its turn
	namer   *discNamer  // asking for a proper name while it rips; nil = not asking
	tracks  *trackAsker // -Q: asking whether it's a foreign film while it rips; nil = not asking
}

func (s *session) handle(ctx context.Context, dev string) error {
	defer s.view.Idle()
	plan, err := s.prepare(ctx, dev)
	if plan == nil {
		return err
	}
	return s.execute(ctx, plan)
}

// prepare reads the disc in dev and decides what to rip, asking the user
// in ask mode. It returns nil if there is nothing to rip: the disc was
// skipped or couldn't be read (recorded, reported and ejected; err is nil or
// errRipFailed), or a fatal error. If the disc's name looks like a code,
// the question asking for its name is started (it is answered whenever,
// without holding up the rip), and in -Q mode, whether it is a foreign film.
func (s *session) prepare(ctx context.Context, dev string) (*plannedRip, error) {
	p, err := s.prepareDisc(ctx, dev)
	if p != nil {
		if s.queue && s.boxSet == nil && s.canAsk {
			p.tracks = &trackAsker{s: s}
		}
		s.askLater(ctx, p)
	}
	return p, err
}

func (s *session) prepareDisc(ctx context.Context, dev string) (*plannedRip, error) {
	var release func()
	holdAsk := func() {
		if release == nil && s.askLock != nil {
			release = s.askLock()
		}
	}
	defer func() {
		if release != nil {
			release()
		}
	}()

	// Writes after Ctrl-C must still reach the database.
	dbctx := context.WithoutCancel(ctx)
	started := s.now()
	driveID, err := s.store.DriveID(dbctx, dev)
	if err != nil {
		return nil, err
	}

	job := store.Job{DriveID: driveID, StartedAt: started, DriveMode: string(s.mode), DrivesUsed: s.drivesUsed}

	doneReading := func() {}
	if s.boxSet != nil {
		doneReading = s.boxSet.startReading()
		defer doneReading()
	}

	s.view.Activity("Reading disc...")
	timeout := s.readTimeout()
	ictx, cancel := ctx, context.CancelFunc(func() {})
	if timeout > 0 {
		ictx, cancel = context.WithTimeout(ctx, timeout)
	}
	stopClock := s.readingClock(timeout)
	info, err := makemkv.RunInfo(ictx, s.cfg.MakeMKVCon, makemkv.DeviceSource(dev), nil)
	if err == nil && len(info.Titles) == 0 && ictx.Err() == nil {
		// MakeMKV couldn't open it. A disc that has only just gone in
		// sometimes isn't quite ready: try once more.
		s.view.Activity("Reading disc again (MakeMKV couldn't open it)...")
		select {
		case <-ictx.Done():
		case <-time.After(readRetryDelay):
			info, err = makemkv.RunInfo(ictx, s.cfg.MakeMKVCon, makemkv.DeviceSource(dev), nil)
		}
	}
	stopClock()
	timedOut := ictx.Err() == context.DeadlineExceeded
	cancel()
	job.ReadTime = s.now().Sub(started)
	switch {
	case ctx.Err() != nil:
		return nil, errInterrupted
	case timedOut:
		return nil, s.failBeforeRip(dbctx, ctx, dev, job,
			fmt.Sprintf("couldn't read the disc: MakeMKV hadn't finished after %s (blurip config set %s to wait longer, 0 for no limit)",
				formatDuration(timeout), config.KeyReadTimeout))
	case err != nil:
		return nil, s.failBeforeRip(dbctx, ctx, dev, job,
			fmt.Sprintf("couldn't read the disc: %v", err))
	case len(info.Titles) == 0:
		// Not recorded as a disc: with nothing read, every unreadable
		// disc would look like the same "Untitled disc".
		reason := "MakeMKV couldn't read the disc"
		if p := info.Problem(); p != "" {
			reason += ": " + p
		}
		return nil, s.failBeforeRip(dbctx, ctx, dev, job, reason)
	}

	name := rip.FolderName(info.Disc.Name(), info.Disc.VolumeName())
	fingerprint := makemkv.Fingerprint(info)
	disc, err := s.store.UpsertDisc(dbctx, store.Disc{
		Fingerprint: fingerprint, Name: name,
		VolumeLabel: info.Disc.VolumeName(), Type: info.Disc.Type(),
	}, started)
	if err != nil {
		return nil, err
	}
	job.DiscID = disc.ID
	var namer *discNamer
	switch {
	case disc.GivenName != "":
		name = disc.GivenName // named by you when it was inserted before
	case s.canAsk && s.boxSet == nil && s.cfg.RipMode != config.RipAsk && rip.LooksLikeCode(name):
		namer = &discNamer{s: s, discID: disc.ID, code: name}
	}
	s.view.SetDisc(name, "")

	title, ok := makemkv.MainFeature(info.Titles)
	if !ok {
		return nil, s.failBeforeRip(dbctx, ctx, dev, job, "MakeMKV found no titles on the disc")
	}

	// Already ripped?
	prev, ripped, err := s.store.LastSuccess(dbctx, disc.ID)
	if err != nil {
		return nil, err
	}
	if ripped {
		when := fmt.Sprintf("%s to %s", prev.StartedAt.Format("2006-01-02 15:04"), prev.OutputPath)
		again := false
		if s.cfg.RipMode == config.RipAsk {
			holdAsk()
			if again, err = s.ask.YesNo(fmt.Sprintf("This disc was ripped successfully on %s. Rip it again?", when), false); err != nil {
				return nil, err
			}
		}
		if !again {
			s.view.Log("– %s: already ripped on %s; skipped", name, when)
			job.FinishedAt, job.Status = s.now(), store.StatusSkipped
			job.FailureReason = fmt.Sprintf("already ripped successfully (job %d)", prev.ID)
			if _, err := s.store.RecordJob(dbctx, job); err != nil {
				return nil, err
			}
			s.eject(ctx, dev)
			return nil, nil
		}
	}

	// Decide what to rip.
	choice := ripChoice{Titles: []makemkv.Title{title}, Name: name}
	switch {
	case s.boxSet != nil:
		if choice, err = s.planBoxSet(ctx, info, name, disc.ID, doneReading, holdAsk); err != nil {
			return nil, err
		}
	case s.cfg.RipMode == config.RipAsk:
		holdAsk()
		if choice, err = s.askWhatToRip(info, title, name); err != nil {
			return nil, err
		}
	default:
		if choice, err = s.checkNotBoxSet(info, name, choice, holdAsk); err != nil {
			return nil, err
		}
	}
	if choice.Skip {
		why := "skipped at your request"
		if choice.SkipWhy != "" {
			why = choice.SkipWhy
		}
		s.view.Log("– %s: %s", name, why)
		job.FinishedAt, job.Status, job.FailureReason = s.now(), store.StatusSkipped, why
		if _, err := s.store.RecordJob(dbctx, job); err != nil {
			return nil, err
		}
		s.eject(ctx, dev)
		return nil, nil
	}
	return &plannedRip{dev: dev, job: job, choice: choice, kind: info.Disc.Kind(), readyAt: s.now(), namer: namer}, nil
}

// execute rips a prepared disc, records the job and ejects the disc.
func (s *session) execute(ctx context.Context, p *plannedRip) error {
	dbctx := context.WithoutCancel(ctx)
	dev, choice, job := p.dev, p.choice, p.job
	ripStart := s.now()
	job.WaitTime = ripStart.Sub(p.readyAt)

	destDir := s.cfg.DestDir(p.kind)
	var nameNow func() string
	if p.namer != nil {
		choice.Name = p.namer.start(destDir)
		nameNow = p.namer.current
	}
	target := filepath.Join(destDir, choice.Name, choice.Name+".mkv")
	if len(choice.Titles) > 1 {
		target = filepath.Join(destDir, choice.Name) + string(filepath.Separator)
	}
	label := choice.Name
	if choice.FileNames != nil {
		destDir = filepath.Join(destDir, choice.Folder)
		target = destDir + string(filepath.Separator)
		label = choice.Episodes
	}
	s.view.SetDisc(label, target)
	if len(choice.Titles) == 1 {
		t := choice.Titles[0]
		s.view.Activity(fmt.Sprintf("Starting: title %d, %s, %s, audio: %s",
			t.ID, t.Duration(), dash(t.SizeHuman()), streamSummary(t.StreamsOfType("Audio"), true)))
	}
	var err error
	if job.ID, err = s.store.StartJob(dbctx, job); err != nil {
		return err
	}
	for _, t := range choice.Titles {
		if err := s.store.AddJobTitle(dbctx, job.ID, t.ID, selectionMakeMKVDefault, selectionMakeMKVDefault); err != nil {
			return err
		}
	}

	rips := s.rips
	if rips == nil {
		rips = &ripCounter{}
	}
	ripID := rips.start()
	res, ripErr := rip.Run(ctx, rip.Request{
		MakeMKVCon: s.cfg.MakeMKVCon, Device: dev, Titles: choice.Titles,
		OutputDir: s.cfg.OutputDir, DestDir: destDir, Name: choice.Name, JobID: fmt.Sprintf("job-%d", job.ID),
		FileNames: choice.FileNames, NameNow: nameNow,
		MaxFillPercent: s.cfg.MaxFillPercent, Reservations: s.reservations,
		OnProgress: s.view.Progress,
		OnTitle: func(i, n int, t makemkv.Title) {
			if n > 1 {
				s.view.ProgressDone()
				what := fmt.Sprintf("Title %d", t.ID)
				if choice.FileNames != nil {
					what = choice.FileNames[i-1]
				}
				s.view.Activity(fmt.Sprintf("%s (%d of %d, %s)", what, i, n, t.Duration()))
			}
		},
	})
	job.RipsAtOnce = rips.finish(ripID)
	s.view.ProgressDone()
	job.FinishedAt = s.now()
	job.RipTime = job.FinishedAt.Sub(ripStart)
	took := formatDuration(job.RipTime)

	switch {
	case ctx.Err() != nil:
		job.Status, job.FailureReason = store.StatusInterrupted, "stopped with Ctrl-C"
	case ripErr != nil:
		job.Status, job.FailureReason = store.StatusFailed, ripErr.Error()
	default:
		job.Status, job.OutputPath, job.BytesWritten = store.StatusSucceeded, res.Dir, res.Bytes
	}
	if err := s.store.FinishJob(dbctx, job); err != nil {
		return fmt.Errorf("recording job %d: %w", job.ID, err)
	}
	if p.namer != nil {
		var placed *rip.Result
		if job.Status == store.StatusSucceeded {
			label, placed = res.Name, &res
		}
		defer p.namer.finish(dbctx, job.ID, placed) // after the ✓ line
	}

	switch job.Status {
	case store.StatusInterrupted:
		s.view.Log("%s: interrupted; the partial rip was removed and the disc left in the drive", label)
		return errInterrupted
	case store.StatusFailed:
		s.view.Log("✗ %s: rip failed after %s: %v", label, took, ripErr)
		s.eject(ctx, dev)
		return errRipFailed
	}
	files := filepath.Join(res.Dir, res.Files[0])
	if len(res.Files) > 1 {
		files = fmt.Sprintf("%s (%d files)", res.Dir, len(res.Files))
	}
	s.view.Log("✓ %s → %s (%s in %s)", label, files, humanize.IBytes(uint64(res.Bytes)), took)
	if s.queue {
		s.queueEncodes(dbctx, p, choice, res, job.ID)
	}
	s.eject(ctx, dev)
	return nil
}

// failBeforeRip records a job that failed before ripping started, then ejects.
func (s *session) failBeforeRip(dbctx, ctx context.Context, dev string, job store.Job, reason string) error {
	job.FinishedAt, job.Status, job.FailureReason = s.now(), store.StatusFailed, reason
	if _, err := s.store.RecordJob(dbctx, job); err != nil {
		return err
	}
	name := "Disc"
	if s.view.disc != "" {
		name = s.view.disc
	}
	s.view.Log("✗ %s: %s", name, reason)
	s.eject(ctx, dev)
	return errRipFailed
}

// eject ejects the disc if configured. A failure is reported but not fatal:
// the rip outcome is what matters and is already recorded.
func (s *session) eject(ctx context.Context, dev string) {
	if !s.cfg.Eject || ctx.Err() != nil {
		return
	}
	s.view.Activity("Ejecting...")
	if err := drive.EjectAndConfirm(ctx, s.ctl, dev, drive.EjectOptions{}); err != nil {
		s.view.Log("⚠ couldn't eject the disc from %s — open that drive by hand: %v", s.view.label, err)
	}
}

// checkSandbox stops blurip before any disc is ripped if MakeMKV runs as a
// Flatpak that can't write to the output folder (see makemkv.CheckSandbox).
// With ask (a terminal), it offers to give the Flatpak access itself.
func checkSandbox(ctx context.Context, cfg config.Config, out io.Writer, ask *prompt.Prompter) error {
	if makemkv.FlatpakAppOf(cfg.MakeMKVCon) == "" || cfg.OutputDir == "" {
		return nil
	}
	fmt.Fprintf(out, "Checking that MakeMKV's Flatpak can use %s...\n", cfg.OutputDir)
	err := makemkv.CheckSandbox(ctx, cfg.MakeMKVCon, cfg.OutputDir)
	var se *makemkv.SandboxError
	if !errors.As(err, &se) || ask == nil {
		return err
	}
	fmt.Fprintf(out, "MakeMKV runs as a Flatpak and isn't allowed to use %s (%s),\nso every rip would fail with \"produced no MKV file\".\n", se.Dir, se.Why)
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
	if err := makemkv.CheckSandbox(ctx, cfg.MakeMKVCon, cfg.OutputDir); err != nil {
		return fmt.Errorf("access was given, but the check still fails: %w", err)
	}
	fmt.Fprintln(out, "Done: MakeMKV can now use the output folder.")
	return nil
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown disc"
	}
	return s
}

// readingClock shows how long the disc has been reading, every second,
// until the returned function is called.
func (s *session) readingClock(limit time.Duration) (stop func()) {
	if s.view == nil {
		return func() {}
	}
	start := time.Now()
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				text := formatDuration(time.Since(start))
				if limit > 0 {
					text += " (gives up at " + formatDuration(limit) + ")"
				}
				s.view.Status(text)
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		s.view.Status("")
	}
}
