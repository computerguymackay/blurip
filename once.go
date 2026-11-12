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
	cfg      config.Config
	store    *store.Store
	ctl      drive.Controller
	ask      *prompt.Prompter
	out      io.Writer
	progress func() *progressLine
	now      func() time.Time
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
	sess := &session{
		cfg: cfg, store: s, ctl: ctl, ask: prompt.NewPrompter(os.Stdin, os.Stdout), out: os.Stdout,
		progress: func() *progressLine { return newProgressLine(os.Stdout) }, now: time.Now,
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
	// Writes after Ctrl-C must still reach the database.
	dbctx := context.WithoutCancel(ctx)
	started := s.now()
	driveID, err := s.store.DriveID(dbctx, dev)
	if err != nil {
		return err
	}

	fmt.Fprintf(s.out, "Reading disc in %s...\n", dev)
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
	fmt.Fprintf(s.out, "%s: %s\n", orUnknown(info.Disc.Type()), name)

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
			if again, err = s.ask.YesNo(fmt.Sprintf("This disc was ripped successfully on %s. Rip it again?", when), false); err != nil {
				return err
			}
		}
		if !again {
			fmt.Fprintf(s.out, "Already ripped on %s; skipping.\n", when)
			job.FinishedAt, job.Status = s.now(), store.StatusSkipped
			job.FailureReason = fmt.Sprintf("already ripped successfully (job %d)", prev.ID)
			if _, err := s.store.RecordJob(dbctx, job); err != nil {
				return err
			}
			s.eject(ctx, dev)
			return nil
		}
	}

	// Rip.
	fmt.Fprintf(s.out, "Main feature: title %d, %s, %s, audio: %s\n",
		title.ID, title.Duration(), dash(title.SizeHuman()), streamSummary(title.StreamsOfType("Audio"), true))
	fmt.Fprintf(s.out, "Ripping to %s\n", filepath.Join(s.cfg.OutputDir, name))
	if job.ID, err = s.store.StartJob(dbctx, disc.ID, driveID, started); err != nil {
		return err
	}
	if err := s.store.AddJobTitle(dbctx, job.ID, title.ID, selectionMakeMKVDefault, selectionMakeMKVDefault); err != nil {
		return err
	}

	pl := s.progress()
	res, ripErr := rip.Run(ctx, rip.Request{
		MakeMKVCon: s.cfg.MakeMKVCon, Device: dev, Title: title,
		OutputDir: s.cfg.OutputDir, Name: name, JobID: fmt.Sprintf("job-%d", job.ID),
		MaxFillPercent: s.cfg.MaxFillPercent, OnProgress: pl.Update,
	})
	pl.Done()
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
		fmt.Fprintln(s.out, "Interrupted; the partial rip was removed. The disc was left in the drive.")
		return errInterrupted
	case store.StatusFailed:
		fmt.Fprintf(s.out, "Rip failed after %s: %v\n", took, ripErr)
		s.eject(ctx, dev)
		return errRipFailed
	}
	fmt.Fprintf(s.out, "Done in %s: %s (%s)\n", took,
		filepath.Join(res.Dir, strings.Join(res.Files, ", ")), humanize.IBytes(uint64(res.Bytes)))
	s.eject(ctx, dev)
	return nil
}

// failBeforeRip records a job that failed before ripping started, then ejects.
func (s *session) failBeforeRip(dbctx, ctx context.Context, dev string, job store.Job, reason string) error {
	job.FinishedAt, job.Status, job.FailureReason = s.now(), store.StatusFailed, reason
	if _, err := s.store.RecordJob(dbctx, job); err != nil {
		return err
	}
	fmt.Fprintln(s.out, "Failed:", reason)
	s.eject(ctx, dev)
	return errRipFailed
}

// eject ejects the disc if configured. A failure is reported but not fatal:
// the rip outcome is what matters and is already recorded.
func (s *session) eject(ctx context.Context, dev string) {
	if !s.cfg.Eject || ctx.Err() != nil {
		return
	}
	fmt.Fprintf(s.out, "Ejecting %s... ", dev)
	if err := drive.EjectAndConfirm(ctx, s.ctl, dev, drive.EjectOptions{}); err != nil {
		fmt.Fprintf(s.out, "failed: %v\n", err)
		return
	}
	fmt.Fprintln(s.out, "done")
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown disc"
	}
	return s
}
