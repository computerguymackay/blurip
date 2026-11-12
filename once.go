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
	devices, err := enabledDevices(ctx, s)
	if err != nil {
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
		cfg: cfg, store: s, ctl: ctl, ask: prompt.NewPrompter(os.Stdin, os.Stdout).WithContext(ctx), out: os.Stdout,
		view: b.Row(ready[0]), now: time.Now,
		askLock: func() func() { b.Pause(); return b.Resume },
	}
	return sess.handleDisc(ctx, ready[0])
}

func enabledDevices(ctx context.Context, s *store.Store) ([]string, error) {
	drives, err := s.Drives(ctx)
	if err != nil {
		return nil, err
	}
	var devices []string
	for _, d := range drives {
		if d.Enabled {
			devices = append(devices, d.Device)
		}
	}
	if len(devices) == 0 {
		return nil, errors.New("no drives enabled; run `blurip setup`")
	}
	return devices, nil
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

func (s *session) handle(ctx context.Context, dev string) error {
	defer s.view.Idle()
	var release func()
	holdAsk := func() {
		if release == nil && s.askLock != nil {
			release = s.askLock()
		}
	}
	endAsk := func() {
		if release != nil {
			release()
			release = nil
		}
	}
	defer endAsk()

	// Writes after Ctrl-C must still reach the database.
	dbctx := context.WithoutCancel(ctx)
	started := s.now()
	driveID, err := s.store.DriveID(dbctx, dev)
	if err != nil {
		return err
	}

	s.view.Activity("Reading disc...")
	info, err := makemkv.RunInfo(ctx, s.cfg.MakeMKVCon, makemkv.DeviceSource(dev), nil)
	if ctx.Err() != nil {
		return errInterrupted
	}
	if err != nil {
		return s.failBeforeRip(dbctx, ctx, dev, store.Job{DriveID: driveID, StartedAt: started},
			fmt.Sprintf("couldn't read the disc: %v", err))
	}

	name := rip.FolderName(info.Disc.Name(), info.Disc.VolumeName())
	disc, err := s.store.UpsertDisc(dbctx, store.Disc{
		Fingerprint: makemkv.Fingerprint(info), Name: name,
		VolumeLabel: info.Disc.VolumeName(), Type: info.Disc.Type(),
	}, started)
	if err != nil {
		return err
	}
	job := store.Job{DiscID: disc.ID, DriveID: driveID, StartedAt: started}
	s.view.SetDisc(name, "")

	title, ok := makemkv.MainFeature(info.Titles)
	if !ok {
		return s.failBeforeRip(dbctx, ctx, dev, job, "MakeMKV found no titles on the disc")
	}

	// Already ripped?
	prev, ripped, err := s.store.LastSuccess(dbctx, disc.ID)
	if err != nil {
		return err
	}
	if ripped {
		when := fmt.Sprintf("%s to %s", prev.StartedAt.Format("2006-01-02 15:04"), prev.OutputPath)
		again := false
		if s.cfg.RipMode == config.RipAsk {
			holdAsk()
			if again, err = s.ask.YesNo(fmt.Sprintf("This disc was ripped successfully on %s. Rip it again?", when), false); err != nil {
				return err
			}
		}
		if !again {
			s.view.Log("– %s: already ripped on %s; skipped", name, when)
			job.FinishedAt, job.Status = s.now(), store.StatusSkipped
			job.FailureReason = fmt.Sprintf("already ripped successfully (job %d)", prev.ID)
			if _, err := s.store.RecordJob(dbctx, job); err != nil {
				return err
			}
			s.eject(ctx, dev)
			return nil
		}
	}

	// Decide what to rip.
	choice := ripChoice{Titles: []makemkv.Title{title}, Name: name}
	if s.cfg.RipMode == config.RipAsk {
		holdAsk()
		if choice, err = s.askWhatToRip(info, title, name); err != nil {
			return err
		}
		if choice.Skip {
			s.view.Log("– %s: skipped at your request", name)
			job.FinishedAt, job.Status, job.FailureReason = s.now(), store.StatusSkipped, "skipped at your request"
			if _, err := s.store.RecordJob(dbctx, job); err != nil {
				return err
			}
			s.eject(ctx, dev)
			return nil
		}
	}

	endAsk()

	// Rip.
	target := filepath.Join(s.cfg.OutputDir, choice.Name, choice.Name+".mkv")
	if len(choice.Titles) > 1 {
		target = filepath.Join(s.cfg.OutputDir, choice.Name) + string(filepath.Separator)
	}
	s.view.SetDisc(choice.Name, target)
	if len(choice.Titles) == 1 {
		t := choice.Titles[0]
		s.view.Activity(fmt.Sprintf("Starting: title %d, %s, %s, audio: %s",
			t.ID, t.Duration(), dash(t.SizeHuman()), streamSummary(t.StreamsOfType("Audio"), true)))
	}
	if job.ID, err = s.store.StartJob(dbctx, disc.ID, driveID, started); err != nil {
		return err
	}
	for _, t := range choice.Titles {
		if err := s.store.AddJobTitle(dbctx, job.ID, t.ID, selectionMakeMKVDefault, selectionMakeMKVDefault); err != nil {
			return err
		}
	}

	res, ripErr := rip.Run(ctx, rip.Request{
		MakeMKVCon: s.cfg.MakeMKVCon, Device: dev, Titles: choice.Titles,
		OutputDir: s.cfg.OutputDir, Name: choice.Name, JobID: fmt.Sprintf("job-%d", job.ID),
		MaxFillPercent: s.cfg.MaxFillPercent, Reservations: s.reservations,
		OnProgress: s.view.Progress,
		OnTitle: func(i, n int, t makemkv.Title) {
			if n > 1 {
				s.view.ProgressDone()
				s.view.Activity(fmt.Sprintf("Title %d (%d of %d, %s)", t.ID, i, n, t.Duration()))
			}
		},
	})
	s.view.ProgressDone()
	job.FinishedAt = s.now()
	took := formatDuration(job.FinishedAt.Sub(started))

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

	switch job.Status {
	case store.StatusInterrupted:
		s.view.Log("%s: interrupted; the partial rip was removed and the disc left in the drive", choice.Name)
		return errInterrupted
	case store.StatusFailed:
		s.view.Log("✗ %s: rip failed after %s: %v", choice.Name, took, ripErr)
		s.eject(ctx, dev)
		return errRipFailed
	}
	files := filepath.Join(res.Dir, res.Files[0])
	if len(res.Files) > 1 {
		files = fmt.Sprintf("%s (%d files)", res.Dir, len(res.Files))
	}
	s.view.Log("✓ %s → %s (%s in %s)", choice.Name, files, humanize.IBytes(uint64(res.Bytes)), took)
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
		s.view.Log("⚠ couldn't eject the disc: %v", err)
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown disc"
	}
	return s
}
