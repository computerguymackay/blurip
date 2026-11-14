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

// boxSetState follows a box set across the discs of one run (--box-set),
// so each disc's episodes carry on from the last disc's. It is shared by
// all drives.
type boxSetState struct {
	mu       sync.Mutex
	started  bool
	show     string
	season   int
	lastDisc int // 0 = unknown
	next     int // next episode number
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

// expectedDisc is the disc number that should come next: 1 at the start
// of a box set, otherwise the one after the last (0 = unknown).
func (bs *boxSetState) expectedDisc(lbl boxset.Label) int {
	switch {
	case !bs.started:
		return 1
	case lbl.Season != 0 && lbl.Season != bs.season:
		return 1 // a new season starts at disc 1
	case bs.lastDisc > 0:
		return bs.lastDisc + 1
	}
	return 0
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

	plan := episodePlan{show: lbl.Show, season: lbl.Season, first: 1, titles: sel.Episodes}
	warnings := sel.Warnings
	confirm := !bs.started
	if !bs.started && lbl.Disc > 1 {
		warnings = append(warnings, fmt.Sprintf("this looks like disc %d, not disc 1: if you're part way through the season, choose Change and enter the first episode on this disc", lbl.Disc))
	}
	if bs.started {
		sameShow := lbl.Show == "" || strings.EqualFold(lbl.Show, bs.show)
		sameSeason := lbl.Season == 0 || lbl.Season == bs.season
		switch {
		case sameShow && sameSeason:
			plan.show, plan.season, plan.first = bs.show, bs.season, bs.next
			if lbl.Disc > 0 && bs.lastDisc > 0 && lbl.Disc != bs.lastDisc+1 {
				warnings = append(warnings, fmt.Sprintf("this looks like disc %d, but the last disc was disc %d (episodes up to %s)",
					lbl.Disc, bs.lastDisc, boxset.Code(bs.season, bs.next-1)))
			}
		default:
			confirm = true // a new show or season starts at episode 1
			if plan.show == "" {
				plan.show = bs.show
			}
		}
	}
	if plan.show == "" {
		plan.show = name
	}
	if plan.season == 0 {
		plan.season = max(bs.season, 1)
	}

	if confirm || len(warnings) > 0 || len(plan.titles) == 0 {
		holdAsk()
		var err error
		if plan, err = askEpisodes(s.ask, s.out, info, lbl, sel, warnings, plan); err != nil {
			return ripChoice{}, err
		}
		if len(plan.titles) == 0 {
			return ripChoice{Skip: true}, nil
		}
	}

	bs.started = true
	bs.show, bs.season = plan.show, plan.season
	bs.next = plan.first + len(plan.titles)
	bs.lastDisc = lbl.Disc
	bs.byDisc[fingerprint] = plan
	bs.notify()
	return plan.choice(name), nil
}

// askEpisodes shows the plan for a box-set disc and lets the user accept
// it, change it, or skip the disc (returned as a plan with no titles).
func askEpisodes(ask *prompt.Prompter, out io.Writer, info *makemkv.Info, lbl boxset.Label, sel boxset.Selection, warnings []string, plan episodePlan) (episodePlan, error) {
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
	if plan.season, err = ask.Int("Season", plan.season, 0, 99); err != nil {
		return plan, err
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
