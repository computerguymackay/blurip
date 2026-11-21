package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/computerguymackay/blurip/internal/rip"
	"github.com/computerguymackay/blurip/internal/store"
)

// trackAsker asks, in -Q mode, whether a film is foreign: if so its encode
// keeps every audio and subtitle track (and, from a Blu-ray, is saved as
// MKV). Like the
// disc-name question, it is asked while the disc rips and answered
// whenever; an answer that comes after the rip was queued updates the
// queued entries, as long as they haven't started encoding.
type trackAsker struct {
	s *session

	mu       sync.Mutex
	answered bool
	all      bool
	jobID    int64 // set once the rip has been queued
}

// question asks it. Call with the questions lock held.
func (a *trackAsker) question(ctx context.Context, name string) {
	s := a.s
	fmt.Fprintf(s.out, "\nWhen %q is encoded, should it keep every audio and subtitle track?\n", name)
	fmt.Fprintln(s.out, "Say yes for a foreign film you watch in its own language. Otherwise it gets your")
	fmt.Fprintln(s.out, "preset's usual tracks.")
	all, err := s.ask.YesNo(fmt.Sprintf("Is %s a foreign film?", name), false)
	if err != nil {
		return // Ctrl-C or no more input: queued as standard, marked unanswered
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.answered, a.all = true, all
	if a.jobID == 0 {
		return
	}
	dbctx := context.WithoutCancel(ctx)
	es, err := s.store.EncodesForJob(dbctx, a.jobID)
	if err != nil {
		s.view.Log("⚠ couldn't update the encode queue: %v", err)
		return
	}
	for _, e := range es {
		if e.Status != store.EncodeWaiting {
			continue
		}
		e.AllTracks, e.TracksAnswered = all, true
		if err := s.store.UpdateEncode(dbctx, e); err != nil {
			s.view.Log("⚠ couldn't update the encode queue: %v", err)
		}
	}
}

// queued records the rip's job and returns the answer so far.
func (a *trackAsker) queued(jobID int64) (all, answered bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.jobID = jobID
	return a.all, a.answered
}

// askLater asks the questions that don't hold up the rip (the disc's
// name, then whether it is foreign), in the background, one after the
// other, waiting their turn behind other questions.
func (s *session) askLater(ctx context.Context, p *plannedRip) {
	if p.namer == nil && p.tracks == nil {
		return
	}
	if s.pending != nil {
		s.pending.Add(1)
	}
	go func() {
		if s.pending != nil {
			defer s.pending.Done()
		}
		release := func() {}
		if s.askLock != nil {
			release = s.askLock()
		}
		defer release()
		name := p.choice.Name
		if p.namer != nil {
			p.namer.question(ctx)
			name = p.namer.current()
		}
		if p.tracks != nil && ctx.Err() == nil {
			p.tracks.question(ctx, name)
		}
	}()
}

// queueEncodes adds a finished rip's files to the encode queue: films go
// to the movies folder, box-set episodes to the shows folder.
func (s *session) queueEncodes(dbctx context.Context, p *plannedRip, choice ripChoice, res rip.Result, jobID int64) {
	all, answered := false, false
	switch {
	case p.tracks != nil:
		all, answered = p.tracks.queued(jobID)
	case choice.AllTracks != nil:
		all, answered = *choice.AllTracks, true
	}
	n := 0
	for _, f := range res.Files {
		if queued, err := s.store.IsQueued(dbctx, filepath.Join(res.Dir, f)); err == nil && queued {
			continue // e.g. a box set disc ripped again before its encode ran
		}
		base := strings.TrimSuffix(f, filepath.Ext(f))
		e := store.Encode{
			JobID: jobID, SourcePath: filepath.Join(res.Dir, f), DiscKind: p.kind,
			Preset: s.cfg.Preset(p.kind), AllTracks: all, TracksAnswered: answered, QueuedAt: s.now(),
		}
		switch {
		case choice.FileNames != nil: // a box-set episode
			e.Title, e.DestDir, e.DestName = base, s.cfg.ShowsDir, filepath.Join(choice.Folder, base)
		case len(res.Files) == 1:
			e.Title, e.DestDir, e.DestName = res.Name, s.cfg.MoviesDir, filepath.Join(res.Name, res.Name)
		default: // several titles of one disc (ask mode)
			e.Title, e.DestDir, e.DestName = res.Name+" - "+base, s.cfg.MoviesDir, filepath.Join(res.Name, base)
		}
		entries := []store.Encode{e}
		if parts := choice.Parts[base]; len(parts) > 0 { // several episodes in one title
			entries = splitEntry(e, parts)
		}
		for _, e := range entries {
			if _, err := s.store.AddEncode(dbctx, e); err != nil {
				s.view.Log("⚠ couldn't add %s to the encode queue: %v", e.Title, err)
				return
			}
			n++
		}
	}
	what := "1 encode"
	if n != 1 {
		what = fmt.Sprintf("%d encodes", n)
	}
	s.view.Log("  queued for encoding: %s", what)
}

// splitEntry makes e, the queue entry for a title holding several
// episodes, into one entry per episode, each encoding its chapters.
func splitEntry(e store.Encode, parts []episodePart) []store.Encode {
	out := make([]store.Encode, len(parts))
	for i, pt := range parts {
		out[i] = e
		out[i].Title, out[i].DestName, out[i].Chapters = pt.Name, filepath.Join(filepath.Dir(e.DestName), pt.Name), pt.Chapters
	}
	return out
}

// renameQueued follows a rip renamed after it was queued (the disc's name
// was given after the rip was moved into place).
func (s *session) renameQueued(dbctx context.Context, jobID int64, old, res rip.Result) {
	es, err := s.store.EncodesForJob(dbctx, jobID)
	if err != nil || len(es) == 0 {
		return
	}
	for _, e := range es {
		if e.Status != store.EncodeWaiting {
			continue
		}
		rel, err := filepath.Rel(old.Dir, e.SourcePath)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		if len(old.Files) == 1 && len(res.Files) == 1 && rel == old.Files[0] {
			rel = res.Files[0]
		}
		e.SourcePath = filepath.Join(res.Dir, rel)
		if len(res.Files) == 1 {
			e.Title, e.DestName = res.Name, filepath.Join(res.Name, res.Name)
		} else {
			base := strings.TrimSuffix(rel, filepath.Ext(rel))
			e.Title, e.DestName = res.Name+" - "+base, filepath.Join(res.Name, base)
		}
		if err := s.store.UpdateEncode(dbctx, e); err != nil {
			s.view.Log("⚠ couldn't update the encode queue: %v", err)
		}
	}
}

// boxSetTracks returns whether a show's episodes keep every track,
// asking once per show (in -Q mode, with a terminal). nil means it wasn't
// answered.
func (s *session) boxSetTracks(dbctx context.Context, show string, holdAsk func()) (*bool, error) {
	if !s.queue {
		return nil, nil
	}
	all, ok, err := s.store.ShowAllTracks(dbctx, show)
	if err != nil || ok {
		return &all, err
	}
	if !s.canAsk {
		return nil, nil
	}
	holdAsk()
	fmt.Fprintf(s.out, "\nWhen %s is encoded, should its episodes keep every audio and subtitle track?\n", show)
	fmt.Fprintln(s.out, "Say yes for a show in a foreign language. You're only asked once per show.")
	if all, err = s.ask.YesNo(fmt.Sprintf("Is %s a foreign-language show?", show), false); err != nil {
		return nil, err
	}
	if err := s.store.SetShowAllTracks(dbctx, show, all); err != nil {
		return nil, err
	}
	return &all, nil
}
