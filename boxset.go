package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/computerguymackay/blurip/internal/boxset"
	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/rip"
)

// boxSetState follows box sets across the discs of one run (--box-set),
// so each disc's episodes carry on from the last disc of the same show and
// season. It is shared by all drives, and can follow several box sets at
// once (one per drive, say).
type boxSetState struct {
	mu      sync.Mutex
	series  map[string]*seriesProgress // by seriesKey(show, season)
	current *seriesProgress            // the last one numbered; for discs whose label names no show
	// Numbering already given to each disc (by fingerprint), so a disc
	// put back in after a failed rip gets the same episode numbers.
	byDisc map[string]episodePlan

	// With several drives, discs are read at the same time and the one
	// read first would be numbered first. reading counts discs still being
	// read; changed is closed (and replaced) whenever reading drops or a
	// disc is numbered, so a disc that is ahead of its turn can wait.
	reading int
	changed chan struct{}
}

// seriesProgress is how far one show's season has got.
type seriesProgress struct {
	show     string
	season   int
	lastDisc int // 0 = unknown
	next     int // next episode number
}

func seriesKey(show string, season int) string {
	return fmt.Sprintf("%s\x00%d", strings.ToLower(show), season)
}

// find returns the progress for show and season, or nil.
func (bs *boxSetState) find(show string, season int) *seriesProgress {
	return bs.series[seriesKey(show, season)]
}

// nextEpisode is the episode a disc of show and season starts at: on from
// the last disc, or 1 for a season not seen yet.
func (bs *boxSetState) nextEpisode(show string, season int) int {
	if p := bs.find(show, season); p != nil {
		return p.next
	}
	return 1
}

// shows returns the distinct shows being followed.
func (bs *boxSetState) shows() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range bs.series {
		if k := strings.ToLower(p.show); !seen[k] {
			seen[k] = true
			out = append(out, p.show)
		}
	}
	return out
}

// identify works out which show and season a disc belongs to. A label
// without a show name is taken to be the show being ripped, but only if
// just one show is; otherwise ok is false and the user has to say.
func (bs *boxSetState) identify(lbl boxset.Label) (show string, season int, ok bool) {
	show, season, ok = lbl.Show, lbl.Season, true
	if show == "" {
		switch len(bs.shows()) {
		case 0:
		case 1:
			show = bs.current.show
		default:
			ok = false
			if bs.current != nil {
				show = bs.current.show // only a suggestion
			}
		}
	}
	if season == 0 {
		season = 1
		if bs.current != nil && strings.EqualFold(bs.current.show, show) {
			season = bs.current.season
		}
	}
	return show, season, ok
}

// expectedDisc is the disc number that should come next for this disc's
// show and season: 1 if it hasn't started, otherwise the one after the
// last (0 = unknown).
func (bs *boxSetState) expectedDisc(lbl boxset.Label) int {
	show, season, _ := bs.identify(lbl)
	p := bs.find(show, season)
	switch {
	case p == nil:
		return 1
	case p.lastDisc > 0:
		return p.lastDisc + 1
	}
	return 0
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

// planBoxSet decides which titles are episodes and how to number them.
// It asks the user on the first disc, when the show or season changes,
// and whenever something looks wrong (a disc out of order, an odd title).
//
// With several drives, a disc that is ahead of its turn (disc 2 read while
// disc 1 is still being read in another drive) waits for the other disc
// to be numbered first, so episodes follow the disc order, not the order
// the drives finish reading.
func (s *session) planBoxSet(ctx context.Context, info *makemkv.Info, name, fingerprint string, doneReading, holdAsk func()) (ripChoice, error) {
	bs := s.boxSet
	doneReading()
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if bs.byDisc == nil {
		bs.byDisc = map[string]episodePlan{}
	}

	if p, ok := bs.byDisc[fingerprint]; ok {
		s.view.Log("%s: same disc as before; using the same episode numbers (%s)", name, p.choice(name).Episodes)
		return p.choice(name), nil
	}

	sel := boxset.Episodes(info.Titles)
	lbl := boxset.ParseLabel(info.Disc.Name(), info.Disc.VolumeName())

	waited := false
	for lbl.Disc > 0 && lbl.Disc > bs.expectedDisc(lbl) && bs.reading > 0 {
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

	show, season, known := bs.identify(lbl)
	if show == "" {
		show = name
	}
	plan := episodePlan{show: show, season: season, first: 1, titles: sel.Episodes}
	warnings := sel.Warnings
	prog := bs.find(show, season)
	confirm := prog == nil || !known // a new show or season is always checked
	switch {
	case !known:
		warnings = append(warnings, fmt.Sprintf("the disc label doesn't say which show this is, and %s are being ripped: check the show name",
			strings.Join(bs.shows(), " and ")))
	case prog != nil:
		plan.first = prog.next
		if lbl.Disc > 0 && prog.lastDisc > 0 && lbl.Disc != prog.lastDisc+1 {
			warnings = append(warnings, fmt.Sprintf("this looks like disc %d, but the last disc of %s season %d was disc %d (episodes up to %s)",
				lbl.Disc, prog.show, prog.season, prog.lastDisc, boxset.Code(prog.season, prog.next-1)))
		}
	case lbl.Disc > 1:
		warnings = append(warnings, fmt.Sprintf("this looks like disc %d, not disc 1: if you're part way through the season, choose Change and enter the first episode on this disc", lbl.Disc))
	}
	if !known && prog != nil {
		plan.first = prog.next
	}

	if confirm || len(warnings) > 0 || len(plan.titles) == 0 {
		holdAsk()
		var err error
		if plan, err = askEpisodes(s.ask, s.out, info, lbl, sel, warnings, plan, bs.nextEpisode); err != nil {
			return ripChoice{}, err
		}
		if len(plan.titles) == 0 {
			return ripChoice{Skip: true}, nil
		}
	}

	if bs.series == nil {
		bs.series = map[string]*seriesProgress{}
	}
	p := bs.find(plan.show, plan.season)
	if p == nil {
		p = &seriesProgress{show: plan.show, season: plan.season}
		bs.series[seriesKey(plan.show, plan.season)] = p
	}
	p.next = max(p.next, plan.first+len(plan.titles))
	p.lastDisc = lbl.Disc
	bs.current = p
	bs.byDisc[fingerprint] = plan
	bs.notify()
	return plan.choice(name), nil
}

// askEpisodes shows the plan for a box-set disc and lets the user accept
// it, change it, or skip the disc (returned as a plan with no titles).
//
// nextEpisode gives the first episode for a show and season, so changing
// them suggests the right number.
func askEpisodes(ask *prompt.Prompter, out io.Writer, info *makemkv.Info, lbl boxset.Label, sel boxset.Selection, warnings []string, plan episodePlan,
	nextEpisode func(show string, season int) int) (episodePlan, error) {
	printEpisodePlan(out, info, lbl, sel, warnings, plan)

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

func printEpisodePlan(out io.Writer, info *makemkv.Info, lbl boxset.Label, sel boxset.Selection, warnings []string, plan episodePlan) {
	label := info.Disc.VolumeName()
	if label == "" {
		label = info.Disc.Name()
	}
	fmt.Fprintf(out, "Box set disc %s", dash(label))
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
	if len(read) > 0 {
		fmt.Fprintf(out, " (read as: %s)", strings.Join(read, ", "))
	}
	fmt.Fprintln(out)

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
