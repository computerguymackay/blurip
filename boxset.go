package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/computerguymackay/blurip/internal/boxset"
	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/rip"
	"github.com/computerguymackay/blurip/internal/store"
)

// boxSetState is what box-set mode (--box-set) keeps in memory during a
// run; each show's progress is in the database (box_sets, disc_episodes).
// It is shared by all drives.
type boxSetState struct {
	mu sync.Mutex
	// The seasons numbered in this run. The first disc of each season in
	// a run is always checked, even when its progress was saved before.
	ripping map[string]series
	current *series // the last one numbered; for discs whose label names no show

	// With several drives, discs are read at the same time and the one
	// read first would be numbered first. reading counts discs still being
	// read; changed is closed (and replaced) whenever reading drops or a
	// disc is numbered, so a disc that is ahead of its turn can wait.
	reading int
	changed chan struct{}

	// active counts, by season folder (lower case), the discs being ripped
	// or waiting to be (see begin). It has its own lock, taken with nothing
	// else waited for while it is held: begin is called by a disc that may
	// be holding the question lock, while another disc holds mu waiting
	// for that question lock to ask its own questions.
	activeMu sync.Mutex
	active   map[string]int
}

// series is a show's season.
type series struct {
	show   string
	season int
}

func (r series) key() string { return fmt.Sprintf("%s\x00%d", strings.ToLower(r.show), r.season) }

// shows returns the distinct shows numbered in this run.
func (bs *boxSetState) shows() []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range bs.ripping {
		if k := strings.ToLower(r.show); !seen[k] {
			seen[k] = true
			out = append(out, r.show)
		}
	}
	return out
}

// identify works out which show and season a disc belongs to. A label
// without a show name is taken to be the show being ripped, but only if
// just one show is; otherwise ok is false and the user has to say.
func (bs *boxSetState) identify(lbl boxset.Label) (r series, ok bool) {
	r, ok = series{lbl.Show, lbl.Season}, true
	if r.show == "" && bs.current != nil {
		r.show = bs.current.show // with several shows, only a suggestion
		ok = len(bs.shows()) <= 1
	}
	if r.season == 0 {
		r.season = 1
		if bs.current != nil && strings.EqualFold(bs.current.show, r.show) {
			r.season = bs.current.season
		}
	}
	return r, ok
}

// notify wakes discs waiting for their turn. Call with mu held.
func (bs *boxSetState) notify() {
	if bs.changed != nil {
		close(bs.changed)
	}
	bs.changed = make(chan struct{})
}

// startReading registers a disc being read and returns the function that
// ends that (safe to call more than once).
func (bs *boxSetState) startReading() func() {
	bs.mu.Lock()
	bs.reading++
	bs.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			bs.mu.Lock()
			bs.reading--
			bs.notify()
			bs.mu.Unlock()
		})
	}
}

// episodePlan is how one disc's episodes are named.
type episodePlan struct {
	show   string
	season int
	first  int
	titles []makemkv.Title
}

func (p episodePlan) choice(fallbackName string) ripChoice {
	names := make([]string, len(p.titles))
	for i := range p.titles {
		names[i] = boxset.FileName(p.show, p.season, p.first+i)
	}
	return ripChoice{
		Titles:    p.titles,
		Name:      fallbackName,
		Folder:    filepath.Join(p.show, boxset.SeasonFolder(p.season)),
		FileNames: names,
		Episodes:  p.show + " " + boxset.Range(p.season, p.first, len(p.titles)),
	}
}

// planBoxSet decides which titles are episodes and how to number them, and
// records the numbering. It shows the plan for every disc (with where a
// saved season had got to, and anything that looks wrong: a disc out of
// order, an odd title) so the titles and numbers can be checked or changed
// before it rips.
//
// With several drives, a disc that is ahead of its turn (disc 2 read while
// disc 1 is still being read in another drive) waits for the other disc
// to be numbered first, so episodes follow the disc order, not the order
// the drives finish reading.
func (s *session) planBoxSet(ctx context.Context, info *makemkv.Info, name string, discID int64, doneReading, holdAsk func()) (ripChoice, error) {
	bs := s.boxSet
	dbctx := context.WithoutCancel(ctx)
	doneReading()
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if bs.ripping == nil {
		bs.ripping = map[string]series{}
	}

	// A disc numbered before (its rip failed or was stopped, or it is being
	// ripped again) keeps its episode numbers.
	if plan, ok, err := s.earlierNumbering(dbctx, info, discID); err != nil {
		return ripChoice{}, err
	} else if ok {
		c := plan.choice(name)
		s.view.Log("%s: numbered before; keeping %s", name, c.Episodes)
		r := series{plan.show, plan.season}
		bs.ripping[r.key()], bs.current = r, &r
		c.AllTracks, err = s.boxSetTracks(dbctx, plan.show, holdAsk)
		return c, err
	}

	sel := boxset.Episodes(info.Titles)
	lbl := boxset.ParseLabel(info.Disc.Name(), info.Disc.VolumeName())

	// A label with no show, season or disc number may well be a film put
	// in by mistake: ask, rather than number it as the next episode.
	noInfo := lbl.Show == "" && lbl.Season == 0 && lbl.Disc == 0
	if noInfo && !s.quiet { // -s: the default answer, no (numbered as episodes)
		holdAsk()
		fmt.Fprintf(s.out, "\n%q doesn't look like part of a box set (no show, season or disc number in its label).\n", name)
		film, err := s.ask.YesNo("Is this a film?", false)
		if err != nil {
			return ripChoice{}, err
		}
		if film {
			return ripChoice{Skip: true,
				SkipWhy: "looks like a film; blurip is in box-set mode, so it wasn't ripped. Rip films with plain `blurip` (without -B)"}, nil
		}
	}
	// A disc labelled with two seasons ("SEASON6AND7") belongs to the
	// later one once that has been started, otherwise to the first.
	twoSeasons, firstSeason := lbl.LastSeason > lbl.Season, lbl.Season
	if twoSeasons && lbl.Show != "" {
		for n := lbl.LastSeason; n > lbl.Season; n-- {
			if _, ok, err := s.store.FindBoxSet(dbctx, lbl.Show, n); err != nil {
				return ripChoice{}, err
			} else if ok {
				lbl.Season = n
				break
			}
		}
	}

	waited := false
	for lbl.Disc > 0 && bs.reading > 0 {
		if expected, err := s.expectedDisc(dbctx, lbl); err != nil || expected == 0 || lbl.Disc <= expected {
			break
		}
		if !waited {
			s.view.Activity(fmt.Sprintf("Disc %d: waiting for the disc being read in another drive to be numbered first", lbl.Disc))
			waited = true
		}
		if bs.changed == nil {
			bs.changed = make(chan struct{})
		}
		ch := bs.changed
		bs.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			bs.mu.Lock()
			return ripChoice{}, errInterrupted
		}
		bs.mu.Lock()
	}

	r, known := bs.identify(lbl)
	if r.show == "" {
		r.show = name
	}
	plan := episodePlan{show: r.show, season: r.season, first: 1, titles: sel.Episodes}
	prog, saved, err := s.store.FindBoxSet(dbctx, r.show, r.season)
	if err != nil {
		return ripChoice{}, err
	}
	_, thisRun := bs.ripping[r.key()]
	var notes []string
	warnings := sel.Warnings
	if saved {
		plan.show, plan.first = prog.Show, prog.NextEpisode
		if !thisRun {
			notes = append(notes, fmt.Sprintf("Carrying on %s season %d from %s (last ripped: %s, %s)", prog.Show, prog.Season,
				boxset.Code(prog.Season, prog.NextEpisode), discWord(prog.LastDisc), prog.UpdatedAt.Local().Format("2 Jan 15:04")))
		}
	}
	if twoSeasons {
		warnings = append(warnings, fmt.Sprintf("the label covers seasons %d and %d, so check the season: this disc is numbered as season %d. If the season changes part way through the disc, rip only the titles of one season (blurip can't split a disc between seasons)",
			firstSeason, lbl.LastSeason, plan.season))
	}
	switch {
	case !known:
		warnings = append(warnings, fmt.Sprintf("the disc label doesn't say which show this is, and %s are being ripped: check the show name",
			strings.Join(bs.shows(), " and ")))
	case saved:
		if lbl.Disc > 0 && prog.LastDisc > 0 && lbl.Disc != prog.LastDisc+1 {
			warnings = append(warnings, fmt.Sprintf("this looks like disc %d, but the last disc of %s season %d was disc %d (episodes up to %s)",
				lbl.Disc, prog.Show, prog.Season, prog.LastDisc, boxset.Code(prog.Season, prog.NextEpisode-1)))
		}
	case lbl.Disc > 1:
		warnings = append(warnings, fmt.Sprintf("this looks like disc %d, not disc 1: if you're part way through the season, choose Change and enter the first episode on this disc", lbl.Disc))
	}

	// Every disc's plan is shown for checking: episode detection can't be
	// trusted. With -s it isn't: the plan as detected is taken.
	if s.quiet {
		for _, w := range warnings {
			s.view.Log("⚠ %s: %s", name, w)
		}
		if len(plan.titles) == 0 {
			return ripChoice{Skip: true, SkipWhy: "no episodes found (not checked: -s)"}, nil
		}
		s.view.Log("%s: ripping as %s %s (not checked: -s)", name, plan.show, boxset.Range(plan.season, plan.first, len(plan.titles)))
	} else {
		holdAsk()
		next := func(show string, season int) int {
			if b, ok, err := s.store.FindBoxSet(dbctx, show, season); err == nil && ok {
				return b.NextEpisode
			}
			return 1
		}
		if plan, err = askEpisodes(s.ask, s.out, info, lbl, sel, notes, warnings, plan, next); err != nil {
			return ripChoice{}, err
		}
		if len(plan.titles) == 0 {
			return ripChoice{Skip: true}, nil
		}
	}

	eps := make([]store.Episode, len(plan.titles))
	for i, t := range plan.titles {
		eps[i] = store.Episode{TitleID: t.ID, Episode: plan.first + i}
	}
	b, err := s.store.AssignEpisodes(dbctx, plan.show, plan.season, lbl.Disc, discID, eps, s.now())
	if err != nil {
		return ripChoice{}, err
	}
	plan.show = b.Show // the saved spelling, so the folder stays the same
	r = series{b.Show, b.Season}
	bs.ripping[r.key()], bs.current = r, &r
	bs.notify()
	c := plan.choice(name)
	c.AllTracks, err = s.boxSetTracks(dbctx, plan.show, holdAsk)
	return c, err
}

// earlierNumbering returns the numbering saved for this disc, if every
// title in it is still on the disc.
func (s *session) earlierNumbering(ctx context.Context, info *makemkv.Info, discID int64) (episodePlan, bool, error) {
	d, ok, err := s.store.EpisodesForDisc(ctx, discID)
	if err != nil || !ok {
		return episodePlan{}, false, err
	}
	byID := map[int]makemkv.Title{}
	for _, t := range info.Titles {
		byID[t.ID] = t
	}
	plan := episodePlan{show: d.BoxSet.Show, season: d.BoxSet.Season, first: d.Episodes[0].Episode}
	for i, e := range d.Episodes {
		t, ok := byID[e.TitleID]
		if !ok || e.Episode != plan.first+i {
			return episodePlan{}, false, nil
		}
		plan.titles = append(plan.titles, t)
	}
	return plan, true, nil
}

// expectedDisc is the disc number that should come next for this disc's
// show and season: 1 if it hasn't started, otherwise the one after the
// last (0 = unknown).
func (s *session) expectedDisc(ctx context.Context, lbl boxset.Label) (int, error) {
	r, _ := s.boxSet.identify(lbl)
	b, ok, err := s.store.FindBoxSet(ctx, r.show, r.season)
	switch {
	case err != nil:
		return 0, err
	case !ok:
		return 1, nil
	case b.LastDisc > 0:
		return b.LastDisc + 1, nil
	}
	return 0, nil
}

func discWord(n int) string {
	if n == 0 {
		return "disc number unknown"
	}
	return fmt.Sprintf("disc %d", n)
}

// askEpisodes shows the plan for a box-set disc and lets the user accept
// it, change it, or skip the disc (returned as a plan with no titles).
//
// nextEpisode gives the first episode for a show and season, so changing
// them suggests the right number.
func askEpisodes(ask *prompt.Prompter, out io.Writer, info *makemkv.Info, lbl boxset.Label, sel boxset.Selection, notes, warnings []string, plan episodePlan,
	nextEpisode func(show string, season int) int) (episodePlan, error) {
	printEpisodePlan(out, info, lbl, sel, notes, warnings, plan)

	def := "rip"
	if len(plan.titles) == 0 {
		def = "change"
	}
	what, err := ask.Choice("Rip this disc like that?", []prompt.Option{
		{Value: "rip", Label: "Yes, rip as " + plan.show + " " + boxset.Range(plan.season, plan.first, len(plan.titles))},
		{Value: "change", Label: "Change the show, season, episode numbers or titles"},
		{Value: "skip", Label: "Skip this disc"},
	}, def)
	if err != nil {
		return plan, err
	}
	switch what {
	case "skip":
		plan.titles = nil
		return plan, nil
	case "rip":
		if len(plan.titles) > 0 {
			return plan, nil
		}
	}

	show, err := ask.String("Show name", plan.show)
	if err != nil {
		return plan, err
	}
	if safe := rip.SafeName(show); safe != "" {
		plan.show = safe
	}
	oldShow, oldSeason := plan.show, plan.season
	if plan.season, err = ask.Int("Season", plan.season, 0, 99); err != nil {
		return plan, err
	}
	if !strings.EqualFold(oldShow, plan.show) || oldSeason != plan.season {
		plan.first = nextEpisode(plan.show, plan.season)
	}
	if plan.first, err = ask.Int("First episode number on this disc", plan.first, 1, 999); err != nil {
		return plan, err
	}
	byID := map[int]makemkv.Title{}
	for _, t := range info.Titles {
		byID[t.ID] = t
	}
	ids, err := ask.Numbers("Titles to rip, in episode order", titleIDList(plan.titles),
		func(id int) bool { _, ok := byID[id]; return ok })
	if err != nil {
		return plan, err
	}
	plan.titles = nil
	for _, id := range ids {
		plan.titles = append(plan.titles, byID[id])
	}
	if len(plan.titles) > 0 {
		fmt.Fprintf(out, "Ripping as %s %s.\n", plan.show, boxset.Range(plan.season, plan.first, len(plan.titles)))
	}
	return plan, nil
}

// labelSummary describes what a disc label says: "Friends, season 1, disc 2".
func labelSummary(lbl boxset.Label) string {
	var read []string
	if lbl.Show != "" {
		read = append(read, lbl.Show)
	}
	if lbl.Season > 0 {
		read = append(read, fmt.Sprintf("season %d", lbl.Season))
	}
	if lbl.Disc > 0 {
		read = append(read, fmt.Sprintf("disc %d", lbl.Disc))
	}
	return strings.Join(read, ", ")
}

// checkNotBoxSet is for normal mode: a disc whose titles look like TV
// episodes would only have its main feature ripped, so ask whether it is a
// box set and, if so, eject it unripped. Without a terminal it is ripped
// as usual, with a warning.
func (s *session) checkNotBoxSet(info *makemkv.Info, name string, main ripChoice, holdAsk func()) (ripChoice, error) {
	sel := boxset.Episodes(info.Titles)
	if !boxset.LooksLikeTV(sel) {
		return main, nil
	}
	what := fmt.Sprintf("%d titles of about %d minutes", len(sel.Episodes), int(sel.Episodes[0].Duration().Round(time.Minute).Minutes()))
	if read := labelSummary(boxset.ParseLabel(info.Disc.Name(), info.Disc.VolumeName())); read != "" {
		what = read + "; " + what
	}
	if !s.canAsk {
		s.view.Log("⚠ %s looks like a TV box-set disc (%s); only the main feature is ripped. Rip box sets with `blurip -B`", name, what)
		return main, nil
	}
	holdAsk()
	fmt.Fprintf(s.out, "\n%q looks like a TV box-set disc (%s).\nblurip isn't in box-set mode, so it would rip only the main feature.\n", name, what)
	tv, err := s.ask.YesNo("Is this a TV box set?", false)
	if err != nil || !tv {
		return main, err
	}
	return ripChoice{Skip: true,
		SkipWhy: "looks like a TV box set; blurip isn't in box-set mode, so it wasn't ripped. Rip box sets with `blurip -B`"}, nil
}

func printEpisodePlan(out io.Writer, info *makemkv.Info, lbl boxset.Label, sel boxset.Selection, notes, warnings []string, plan episodePlan) {
	label := info.Disc.VolumeName()
	if label == "" {
		label = info.Disc.Name()
	}
	fmt.Fprintf(out, "Box set disc %s", dash(label))
	if read := labelSummary(lbl); read != "" {
		fmt.Fprintf(out, " (read as: %s)", read)
	}
	fmt.Fprintln(out)
	for _, n := range notes {
		fmt.Fprintf(out, "  %s\n", n)
	}

	if len(plan.titles) > 0 {
		fmt.Fprintf(out, "  Episodes (%d), going to %s:\n", len(plan.titles), filepath.Join(plan.show, boxset.SeasonFolder(plan.season)))
		for i, t := range plan.titles {
			fmt.Fprintf(out, "    title %-3d %8s  %9s  → %s\n", t.ID, t.Duration(), dash(t.SizeHuman()), boxset.FileName(plan.show, plan.season, plan.first+i))
		}
	}
	if len(sel.Left) > 0 {
		fmt.Fprintln(out, "  Left out:")
		for _, l := range sel.Left {
			fmt.Fprintf(out, "    title %-3d %8s  %9s  %s\n", l.Title.ID, l.Title.Duration(), dash(l.Title.SizeHuman()), l.Reason)
		}
	}
	for _, w := range warnings {
		fmt.Fprintf(out, "  ⚠ %s\n", w)
	}
	fmt.Fprintln(out)
}

func titleIDList(ts []makemkv.Title) []int {
	ids := make([]int, len(ts))
	for i, t := range ts {
		ids[i] = t.ID
	}
	return ids
}
