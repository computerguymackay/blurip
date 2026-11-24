package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
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

// isCurrent reports whether show is the one this run is ripping.
func (bs *boxSetState) isCurrent(show string) bool {
	return bs.current != nil && strings.EqualFold(bs.current.show, show)
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
	// splits holds, by title ID, the chapters of each episode in a title
	// that holds several (one long title with every episode in it).
	splits map[int][]boxset.Chapters
}

// parts is how many episodes title id is.
func (p episodePlan) parts(id int) int { return max(1, len(p.splits[id])) }

// count is how many episodes the disc has.
func (p episodePlan) count() int {
	n := 0
	for _, t := range p.titles {
		n += p.parts(t.ID)
	}
	return n
}

// episodes returns how the titles are numbered, for AssignEpisodes.
func (p episodePlan) episodes() []store.Episode {
	eps := make([]store.Episode, len(p.titles))
	ep := p.first
	for i, t := range p.titles {
		eps[i] = store.Episode{TitleID: t.ID, Episode: ep, Chapters: boxset.FormatChapters(p.splits[t.ID])}
		ep += p.parts(t.ID)
	}
	return eps
}

// setSplit records title id's split (none: not split).
func (p *episodePlan) setSplit(id int, cs []boxset.Chapters) {
	if len(cs) <= 1 {
		delete(p.splits, id)
		return
	}
	if p.splits == nil {
		p.splits = map[int][]boxset.Chapters{}
	}
	p.splits[id] = cs
}

// episodePart is one episode of a title holding several: its name and
// HandBrake's chapter range for it.
type episodePart struct {
	Name     string // "Spaced - S01E03"
	Chapters string // "11-15"
}

func (p episodePlan) choice(fallbackName string) ripChoice {
	c := ripChoice{
		Titles:   p.titles,
		Name:     fallbackName,
		Folder:   filepath.Join(p.show, boxset.SeasonFolder(p.season)),
		Episodes: p.show + " " + boxset.Range(p.season, p.first, p.count()),
	}
	ep := p.first
	for _, t := range p.titles {
		n := p.parts(t.ID)
		name := boxset.FileNameRange(p.show, p.season, ep, n)
		c.FileNames = append(c.FileNames, name)
		for i, ch := range p.splits[t.ID] {
			if c.Parts == nil {
				c.Parts = map[string][]episodePart{}
			}
			c.Parts[name] = append(c.Parts[name], episodePart{Name: boxset.FileName(p.show, p.season, ep+i), Chapters: ch.String()})
		}
		ep += n
	}
	return c
}

// askEpisodeCount asks how many episodes a title holds, listing the counts
// that share its chapters out evenly (the same length can be 5 episodes of
// 5 chapters or 25 of one, so it can't be told from the length alone).
// The default is cur (the split so far), else a guess from usual episode
// lengths, else one episode.
func askEpisodeCount(ask *prompt.Prompter, t makemkv.Title, cur int) (int, error) {
	total, d := t.Chapters(), t.Duration()
	about := func(n int) string { return (d / time.Duration(n)).Round(time.Second).String() }
	counts := boxset.EvenSplits(d, total)
	def := cur
	if def == 0 {
		def = boxset.GuessEpisodes(d, total)
	}
	if def > 1 && !slices.Contains(counts, def) {
		counts = append(counts, def)
		slices.Sort(counts)
	}
	opts := []prompt.Option{{Value: "1", Label: "1 episode (don't split it)"}}
	for _, n := range counts {
		label := fmt.Sprintf("%d episodes of %d chapters, about %s each", n, total/n, about(n))
		switch {
		case total%n != 0:
			label = fmt.Sprintf("%d episodes, about %s each (chapters shared out unevenly)", n, about(n))
		case total == n:
			label = fmt.Sprintf("%d episodes, one chapter each, about %s each", n, about(n))
		}
		opts = append(opts, prompt.Option{Value: strconv.Itoa(n), Label: label})
	}
	opts = append(opts, prompt.Option{Value: "other", Label: "Another number of episodes"})
	what, err := ask.Choice(fmt.Sprintf("How many episodes are in title %d (%s, %d chapters)?", t.ID, d, total), opts, strconv.Itoa(max(1, def)))
	if err != nil {
		return 0, err
	}
	if what != "other" {
		return strconv.Atoi(what)
	}
	return ask.Int("How many episodes?", max(2, def), 1, total)
}

// askSplit asks how many episodes a title holds and which chapters each
// is, suggesting the chapters shared out evenly; cur is its split so far.
// One episode (or none) means it isn't split.
func askSplit(ask *prompt.Prompter, out io.Writer, t makemkv.Title, cur []boxset.Chapters) ([]boxset.Chapters, error) {
	total := t.Chapters()
	n, err := askEpisodeCount(ask, t, len(cur))
	if err != nil || n <= 1 {
		return nil, err
	}
	cs := cur
	if len(cs) != n {
		cs = boxset.SplitEvenly(total, n)
	}
	for {
		fmt.Fprintf(out, "  %d episodes of about %s:\n", n, (t.Duration() / time.Duration(n)).Round(time.Second))
		for i, c := range cs {
			fmt.Fprintf(out, "    episode %d: chapters %s\n", i+1, c)
		}
		what, err := ask.Choice("Split it like that?", []prompt.Option{
			{Value: "yes", Label: "Yes"},
			{Value: "change", Label: "Change the chapters of each episode"},
			{Value: "no", Label: "Don't split it: it's one episode"},
		}, "yes")
		switch {
		case err != nil:
			return nil, err
		case what == "yes":
			return cs, nil
		case what == "no":
			return nil, nil
		}
		for {
			s, err := ask.String("Chapters of each episode, in order (e.g. 1-5,6-11,12-16)", boxset.FormatChapters(cs))
			if err != nil {
				return nil, err
			}
			next, err := boxset.ParseChapters(s, total)
			if err != nil {
				fmt.Fprintf(out, "  %v\n", err)
				continue
			}
			if len(next) > 0 && next[len(next)-1].Last < total {
				fmt.Fprintf(out, "  Chapters %d-%d aren't in any episode: they'll be left out of the encodes.\n", next[len(next)-1].Last+1, total)
			}
			cs, n = next, max(1, len(next))
			break
		}
		if n == 1 {
			return nil, nil
		}
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
	var guessed string // why the show and season came from the database
	if lbl.Season == 0 && !s.quiet {
		if g, why, err := s.guessSeries(dbctx, r); err != nil {
			return ripChoice{}, err
		} else if why != "" {
			r, guessed = g, why
		}
	}
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
		if guessed != "" {
			notes = append(notes, guessed)
		}
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
	cand, splittable := boxset.SplitCandidate(sel)
	if s.quiet {
		if splittable {
			warnings = append(warnings, fmt.Sprintf("title %d (%s, %d chapters) may be several episodes in one; it is ripped as one episode (not checked: -s)",
				cand.ID, cand.Duration(), cand.Chapters()))
		}
		for _, w := range warnings {
			s.view.Log("⚠ %s: %s", name, w)
		}
		if len(plan.titles) == 0 {
			return ripChoice{Skip: true, SkipWhy: "no episodes found (not checked: -s)"}, nil
		}
		s.view.Log("%s: ripping as %s %s (not checked: -s)", name, plan.show, boxset.Range(plan.season, plan.first, plan.count()))
	} else {
		holdAsk()
		if splittable {
			fmt.Fprintf(s.out, "\n%s: title %d is the only episode-length title, and is %s long with %d chapters: it may be several episodes in one.\n",
				name, cand.ID, cand.Duration(), cand.Chapters())
			cs, err := askSplit(s.ask, s.out, cand, nil)
			if err != nil {
				return ripChoice{}, err
			}
			plan.setSplit(cand.ID, cs)
			if len(cs) > 0 {
				warnings = slices.DeleteFunc(warnings, func(w string) bool { return w == boxset.OnlyOneEpisode })
			}
		}
		next := func(show string, season int) int {
			if b, ok, err := s.store.FindBoxSet(dbctx, show, season); err == nil && ok {
				return b.NextEpisode
			}
			return 1
		}
		last := func(show string) (store.BoxSet, bool) {
			b, ok, err := s.lastBoxSet(dbctx, show)
			return b, ok && err == nil
		}
		if plan, err = askEpisodes(s.ask, s.out, info, lbl, sel, notes, warnings, plan, next, last, s.queue); err != nil {
			return ripChoice{}, err
		}
		if len(plan.titles) == 0 {
			return ripChoice{Skip: true}, nil
		}
	}

	b, err := s.store.AssignEpisodes(dbctx, plan.show, plan.season, lbl.Disc, discID, plan.episodes(), s.now())
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

// lastBoxSet is the season of show ripped most recently ("" = of any
// show).
func (s *session) lastBoxSet(ctx context.Context, show string) (store.BoxSet, bool, error) {
	all, err := s.store.BoxSets(ctx) // most recently updated first
	if err != nil {
		return store.BoxSet{}, false, err
	}
	for _, b := range all {
		if show == "" || strings.EqualFold(b.Show, show) {
			return b, true, nil
		}
	}
	return store.BoxSet{}, false, nil
}

// guessSeries is for a disc whose label doesn't say the season (often
// not the show either, or not as it was typed in): it carries on the
// show's season ripped last or, for a show not ripped before (or none),
// the box set being ripped, or else the one ripped last. why is the note
// shown with the plan ("" = no better guess than r).
func (s *session) guessSeries(ctx context.Context, r series) (series, string, error) {
	if r.show != "" {
		b, ok, err := s.lastBoxSet(ctx, r.show)
		if err != nil || ok {
			if ok && b.Season != r.season && !s.boxSet.isCurrent(b.Show) {
				return series{b.Show, b.Season}, fmt.Sprintf("The disc label doesn't say the season: carrying on season %d, the last of %s ripped", b.Season, b.Show), nil
			}
			return r, "", err
		}
	}
	if c := s.boxSet.current; c != nil {
		if strings.EqualFold(c.show, r.show) {
			return r, "", nil
		}
		return *c, fmt.Sprintf("The disc label doesn't say which show this is: carrying on %s season %d, as the disc before (choose Change if it's another show)", c.show, c.season), nil
	}
	b, ok, err := s.lastBoxSet(ctx, "")
	if err != nil || !ok {
		return r, "", err
	}
	return series{b.Show, b.Season}, fmt.Sprintf("The disc label doesn't say which show this is: carrying on %s season %d, the box set ripped last (choose Change if it's another show)", b.Show, b.Season), nil
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
	ep := plan.first
	for _, e := range d.Episodes {
		t, ok := byID[e.TitleID]
		if !ok || e.Episode != ep {
			return episodePlan{}, false, nil
		}
		cs, err := boxset.ParseChapters(e.Chapters, 0)
		if err != nil {
			return episodePlan{}, false, nil
		}
		plan.titles = append(plan.titles, t)
		plan.setSplit(t.ID, cs)
		ep += e.Parts()
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
// nextEpisode gives the first episode for a show and season, and
// lastSeason the season of a show ripped last, so changing them suggests
// where the show got to.
func askEpisodes(ask *prompt.Prompter, out io.Writer, info *makemkv.Info, lbl boxset.Label, sel boxset.Selection, notes, warnings []string, plan episodePlan,
	nextEpisode func(show string, season int) int, lastSeason func(show string) (store.BoxSet, bool), queue bool) (episodePlan, error) {
	printEpisodePlan(out, info, lbl, sel, notes, warnings, plan, queue)

	def := "rip"
	if len(plan.titles) == 0 {
		def = "change"
	}
	what, err := ask.Choice("Rip this disc like that?", []prompt.Option{
		{Value: "rip", Label: "Yes, rip as " + plan.show + " " + boxset.Range(plan.season, plan.first, plan.count())},
		{Value: "change", Label: "Change the show, season, episode numbers, titles or how a title splits into episodes"},
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

	oldShow, oldSeason := plan.show, plan.season
	show, err := ask.String("Show name", plan.show)
	if err != nil {
		return plan, err
	}
	if safe := rip.SafeName(show); safe != "" {
		plan.show = safe
	}
	// Another show: suggest the season of it ripped last, if any.
	if !strings.EqualFold(oldShow, plan.show) {
		if b, ok := lastSeason(plan.show); ok {
			plan.show, plan.season = b.Show, b.Season
		}
	}
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
	old := plan.splits
	plan.titles, plan.splits = nil, nil
	var shortest time.Duration
	for _, id := range ids {
		if d := byID[id].Duration(); shortest == 0 || d < shortest {
			shortest = d
		}
	}
	for _, id := range ids {
		t := byID[id]
		plan.titles = append(plan.titles, t)
		// A long title with chapters may be several episodes in one: one
		// already split, the only title, or one much longer than the rest.
		if boxset.MightHoldEpisodes(t) && (len(old[id]) > 0 || len(ids) == 1 || t.Duration() >= shortest*17/10) {
			cs, err := askSplit(ask, out, t, old[id])
			if err != nil {
				return plan, err
			}
			plan.setSplit(id, cs)
		}
	}
	if len(plan.titles) > 0 {
		fmt.Fprintf(out, "Ripping as %s %s.\n", plan.show, boxset.Range(plan.season, plan.first, plan.count()))
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

func printEpisodePlan(out io.Writer, info *makemkv.Info, lbl boxset.Label, sel boxset.Selection, notes, warnings []string, plan episodePlan, queue bool) {
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
		fmt.Fprintf(out, "  Episodes (%d), going to %s:\n", plan.count(), filepath.Join(plan.show, boxset.SeasonFolder(plan.season)))
		ep := plan.first
		for _, t := range plan.titles {
			cs := plan.splits[t.ID]
			if len(cs) == 0 {
				fmt.Fprintf(out, "    title %-3d %8s  %9s  → %s\n", t.ID, t.Duration(), dash(t.SizeHuman()), boxset.FileName(plan.show, plan.season, ep))
				ep++
				continue
			}
			fmt.Fprintf(out, "    title %-3d %8s  %9s  → %s, split into %d episodes when encoded:\n", t.ID, t.Duration(), dash(t.SizeHuman()),
				boxset.FileNameRange(plan.show, plan.season, ep, len(cs)), len(cs))
			for _, c := range cs {
				fmt.Fprintf(out, "        chapters %-7s → %s\n", c, boxset.FileName(plan.show, plan.season, ep))
				ep++
			}
			if !queue {
				fmt.Fprintln(out, "        (ripped as one file; to encode it as episodes, queue it with blurip encode -U)")
			}
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
