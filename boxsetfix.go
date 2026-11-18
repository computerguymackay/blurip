package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/computerguymackay/blurip/internal/boxset"
	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/rip"
	"github.com/computerguymackay/blurip/internal/store"
)

// A box set disc that was ripped before and is put in again (in box-set
// mode, with a terminal) gets a menu: skip it, rip it all again, or fix
// it. Fixing it means choosing its titles and their order again: titles
// already ripped are renamed to their new episode numbers (rips, encoded
// files and the encode queue), only titles not ripped before are ripped,
// and titles left out are deleted. If the disc ends up with more or fewer
// episodes, every later episode of the season is renumbered to match.
// Nothing is renamed or deleted until the new titles have been ripped.

// fixStep is one title of the disc after the fix.
type fixStep struct {
	title makemkv.Title
	ep    int  // its episode after the fix
	oldEp int  // its episode before; 0 = it wasn't one
	rip   bool // rip it (new, or nothing of it is left, or the whole disc again)
}

// fixPlan is how a box set disc is to be fixed or ripped again.
type fixPlan struct {
	whole   bool // rip the whole disc again, same episodes
	box     store.BoxSet
	discID  int64
	discNo  int
	steps   []fixStep // in episode order
	dropped []fixStep // titles that were episodes and no longer are
	first   int       // the disc's first episode before the fix
	start   int       // its first episode after (first, unless a gap before it is closed)
	after   int       // the disc's last episode before the fix
	delta   int       // how far every later episode moves (episodes gained or lost, less any gap closed)
	prevEnd int       // the last episode of the disc before it (0 = none)
	nextAt  int       // the first episode of the disc after it (0 = none)
	close   bool      // close the gaps next to it
	later   []store.NumberedEpisode
	ripDir  string // the season's rip folder
	folder  string // "Show/Season 01"
}

func (p *fixPlan) base(ep int) string { return boxset.FileName(p.box.Show, p.box.Season, ep) }

func (p *fixPlan) ripPath(ep int) string { return filepath.Join(p.ripDir, p.base(ep)+".mkv") }

func (p *fixPlan) code(ep int) string { return boxset.Code(p.box.Season, ep) }

// ripSteps returns the titles to rip and their file names.
func (p *fixPlan) ripSteps() ([]makemkv.Title, []string) {
	var ts []makemkv.Title
	var names []string
	for _, st := range p.steps {
		if st.rip {
			ts = append(ts, st.title)
			names = append(names, p.base(st.ep))
		}
	}
	return ts, names
}

// episodeDirs are the folders an episode's files can be in: the season's
// rip folder and its folder in the shows folder.
func (s *session) episodeDirs(p *fixPlan) []string {
	dirs := []string{p.ripDir}
	if s.cfg.ShowsDir != "" {
		dirs = append(dirs, filepath.Join(s.cfg.ShowsDir, p.folder))
	}
	return dirs
}

// episodeFiles returns the files of episode ep: those named after it
// ("Show - S01E05.mkv", "Show - S01E05.en.srt", "Show - S01E05 (2).mp4")
// in dirs, plus any encode of its rip recorded elsewhere. inRip says
// whether each is in the rip folder.
func (s *session) episodeFiles(ctx context.Context, p *fixPlan, ep int, dirs []string) (files []string, inRip map[string]bool, err error) {
	base := p.base(ep)
	seen := map[string]bool{}
	inRip = map[string]bool{}
	add := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || !(strings.HasPrefix(n, base+".") || strings.HasPrefix(n, base+" (")) {
				continue
			}
			f := filepath.Join(dir, n)
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
				inRip[f] = dir == p.ripDir
			}
		}
	}
	for _, d := range dirs {
		add(d)
	}
	es, err := s.store.EncodesForSource(ctx, p.ripPath(ep))
	if err != nil {
		return nil, nil, err
	}
	for _, e := range es {
		if e.OutputPath != "" {
			add(filepath.Dir(e.OutputPath))
		}
	}
	return files, inRip, nil
}

// episodeState says what is left of an episode: its rip, and an encode.
func (s *session) episodeState(ctx context.Context, p *fixPlan, ep int) (ripped, encoded bool, err error) {
	files, inRip, err := s.episodeFiles(ctx, p, ep, s.episodeDirs(p))
	for _, f := range files {
		if inRip[f] {
			ripped = ripped || strings.EqualFold(filepath.Ext(f), ".mkv")
		} else if ext := strings.ToLower(filepath.Ext(f)); ext == ".mkv" || ext == ".mp4" || ext == ".m4v" {
			encoded = true
		}
	}
	return ripped, encoded, err
}

// newFixPlan starts the plan for a numbered disc, if every one of its
// numbered titles is in titles and its episodes run on from one another;
// order is its titles in episode order, oldEp each one's episode.
func newFixPlan(d store.DiscEpisodes, discID int64, ripDir string, titles map[int]makemkv.Title) (p *fixPlan, order []int, oldEp map[int]int, ok bool) {
	p = &fixPlan{box: d.BoxSet, discID: discID, discNo: d.DiscNo, first: d.Episodes[0].Episode,
		folder: filepath.Join(d.BoxSet.Show, boxset.SeasonFolder(d.BoxSet.Season)), ripDir: ripDir}
	oldEp = map[int]int{}
	for i, e := range d.Episodes {
		if _, ok := titles[e.TitleID]; !ok || e.Episode != p.first+i {
			return nil, nil, nil, false
		}
		oldEp[e.TitleID] = e.Episode
		order = append(order, e.TitleID)
	}
	p.start, p.after = p.first, p.first+len(order)-1
	return p, order, oldEp, true
}

// titleLen is a title's running time, or "" when it isn't known (fixing
// a disc that isn't in the drive).
func titleLen(t makemkv.Title) string {
	if d := t.Duration(); d > 0 {
		return d.String()
	}
	return ""
}

// printEpisodes lists titles with the episode each became and what is
// left of it.
func (s *session) printEpisodes(ctx context.Context, p *fixPlan, titles []makemkv.Title, oldEp map[int]int) error {
	for _, t := range titles {
		ep, ok := oldEp[t.ID]
		if !ok {
			fmt.Fprintf(s.out, "    title %-3d %8s  %9s   not an episode\n", t.ID, titleLen(t), dash(t.SizeHuman()))
			continue
		}
		ripped, encoded, err := s.episodeState(ctx, p, ep)
		if err != nil {
			return err
		}
		fmt.Fprintf(s.out, "    title %-3d %8s  %9s → %s  rip %s  encoded %s\n", t.ID, titleLen(t), dash(t.SizeHuman()),
			p.code(ep), tick(ripped), tick(encoded))
	}
	return nil
}

// askFix asks for the titles in episode order, shows the plan and asks
// to go ahead. ok false means don't (why says why).
func (s *session) askFix(ctx context.Context, p *fixPlan, order []int, byID map[int]makemkv.Title, oldEp map[int]int,
	check func(*fixPlan) error) (ok bool, why string, err error) {
	if err := s.findGaps(ctx, p); err != nil {
		return false, "", err
	}
	if gaps := p.gapText(); gaps != "" {
		fmt.Fprintf(s.out, "\n%s.\n", gaps)
		if p.close, err = s.ask.YesNo("Close the gap (renumber the episodes so they run on)?", true); err != nil {
			return false, "", err
		}
		if p.close && p.prevEnd > 0 {
			p.start = p.prevEnd + 1
		}
	}
	ids, err := s.ask.Numbers(fmt.Sprintf("Titles in episode order (the first is %s)", p.code(p.start)), order,
		func(id int) bool { _, ok := byID[id]; return ok })
	if err != nil {
		return false, "", err
	}
	if err := s.planFix(ctx, p, ids, byID, oldEp); err != nil {
		return false, "", err
	}
	if !p.changes() {
		fmt.Fprintln(s.out, "That's how it is already: nothing to change.")
		return false, "already ripped; nothing to change", nil
	}
	if check != nil {
		if err := check(p); err != nil {
			fmt.Fprintf(s.out, "%v\n", err)
			return false, err.Error(), nil
		}
	}
	s.printFix(p)
	if ok, err = s.ask.YesNo("Go ahead?", false); err != nil || !ok {
		return false, "fixing it was cancelled", err
	}
	return true, "", nil
}

// askDoneBoxSetDisc shows a box set disc that was ripped before and asks
// what to do with it. It returns nil (and why) to skip it.
func (s *session) askDoneBoxSetDisc(ctx context.Context, info *makemkv.Info, name string, d store.DiscEpisodes,
	discID int64, prev store.Job, holdAsk func()) (*fixPlan, string, error) {
	byID := map[int]makemkv.Title{}
	for _, t := range info.Titles {
		byID[t.ID] = t
	}
	var p *fixPlan
	var order []int
	var oldEp map[int]int
	var what string
	for {
		ripDir := prev.OutputPath
		if ripDir == "" {
			ripDir = filepath.Join(s.cfg.DestDir(info.Disc.Kind()), d.BoxSet.Show, boxset.SeasonFolder(d.BoxSet.Season))
		}
		var ok bool
		p, order, oldEp, ok = newFixPlan(d, discID, ripDir, byID)
		if !ok {
			s.view.Log("– %s: already ripped as %s, but its titles don't match how it was numbered, so it can't be fixed; skipped",
				name, episodeSummary(ctx, s.store, discID))
			return nil, "already ripped; its titles don't match how it was numbered", nil
		}

		holdAsk()
		fmt.Fprintf(s.out, "\n%s was ripped on %s as %s %s.\n", name, prev.StartedAt.Format("2006-01-02 15:04"),
			p.box.Show, boxset.Range(p.box.Season, p.first, len(order)))
		if err := s.printEpisodes(ctx, p, info.Titles, oldEp); err != nil {
			return nil, "", err
		}
		var err error
		what, err = s.ask.Choice("What now?", []prompt.Option{
			{Value: "skip", Label: "Skip it"},
			{Value: "again", Label: "Rip the whole disc again (same episodes; the rips are replaced)"},
			{Value: "fix", Label: "Fix it: choose its titles and their order again; only titles not ripped before are ripped"},
			{Value: "rename", Label: fmt.Sprintf("Rename the show or change the season (all of %s season %d)", p.box.Show, p.box.Season)},
		}, "skip")
		if err != nil {
			return nil, "", err
		}
		if what != "rename" {
			break
		}
		done, err := s.askSeasonRename(ctx, p.box)
		if err != nil {
			return nil, "", err
		}
		if done == "" {
			continue
		}
		s.view.Log("%s", done)
		// Back to the menu, with the new names.
		if d, _, err = s.store.EpisodesForDisc(ctx, discID); err != nil {
			return nil, "", err
		}
		if prev, _, err = s.store.LastSuccess(ctx, discID); err != nil {
			return nil, "", err
		}
	}
	switch what {
	case "skip":
		return nil, fmt.Sprintf("already ripped successfully (job %d); skipped at your request", prev.ID), nil
	case "again":
		p.whole = true
		for _, id := range order {
			p.steps = append(p.steps, fixStep{title: byID[id], ep: oldEp[id], oldEp: oldEp[id], rip: true})
		}
		return p, "", nil
	}
	ok, why, err := s.askFix(ctx, p, order, byID, oldEp, nil)
	if err != nil || !ok {
		return nil, why, err
	}
	return p, "", nil
}

func tick(b bool) string {
	if b {
		return "✓"
	}
	return "–"
}

// planFix works out the fix for the titles ids, in episode order.
func (s *session) planFix(ctx context.Context, p *fixPlan, ids []int, byID map[int]makemkv.Title, oldEp map[int]int) error {
	kept := map[int]bool{}
	for i, id := range ids {
		st := fixStep{title: byID[id], ep: p.start + i, oldEp: oldEp[id]}
		if st.oldEp == 0 {
			st.rip = true
		} else {
			// Nothing left of it (rip and encode both deleted): rip it.
			files, _, err := s.episodeFiles(ctx, p, st.oldEp, s.episodeDirs(p))
			if err != nil {
				return err
			}
			st.rip = len(files) == 0
		}
		p.steps = append(p.steps, st)
		kept[id] = true
	}
	for id, ep := range oldEp {
		if !kept[id] {
			p.dropped = append(p.dropped, fixStep{title: byID[id], oldEp: ep})
		}
	}
	slices.SortFunc(p.dropped, func(a, b fixStep) int { return a.oldEp - b.oldEp })
	last := p.start + len(ids) - 1
	p.delta = last - p.after
	if p.close && p.nextAt > 0 {
		p.delta = last + 1 - p.nextAt
	}
	if p.delta != 0 {
		all, err := s.store.BoxSetEpisodes(ctx, p.box.ID)
		if err != nil {
			return err
		}
		for _, e := range all {
			if e.DiscID != p.discID && e.Episode > p.after {
				p.later = append(p.later, e)
			}
		}
	}
	return nil
}

// findGaps finds the discs either side of the plan's disc: where the one
// before ends and the one after starts.
func (s *session) findGaps(ctx context.Context, p *fixPlan) error {
	all, err := s.store.BoxSetEpisodes(ctx, p.box.ID)
	if err != nil {
		return err
	}
	for _, e := range all {
		switch {
		case e.DiscID == p.discID:
		case e.Episode < p.first && e.Episode > p.prevEnd:
			p.prevEnd = e.Episode
		case e.Episode > p.after && (p.nextAt == 0 || e.Episode < p.nextAt):
			p.nextAt = e.Episode
		}
	}
	return nil
}

// gapText describes any episodes missing between the plan's disc and
// the discs either side of it ("" = none).
func (p *fixPlan) gapText() string {
	missing := func(from, to int) string {
		if from == to {
			return p.code(from) + " is missing"
		}
		return fmt.Sprintf("%s–%s are missing", p.code(from), p.code(to))
	}
	var gaps []string
	if p.prevEnd > 0 && p.first > p.prevEnd+1 {
		gaps = append(gaps, fmt.Sprintf("%s: the disc before ends at %s and this one starts at %s",
			missing(p.prevEnd+1, p.first-1), p.code(p.prevEnd), p.code(p.first)))
	}
	if p.nextAt > p.after+1 {
		gaps = append(gaps, fmt.Sprintf("%s: this disc ends at %s and the disc after starts at %s",
			missing(p.after+1, p.nextAt-1), p.code(p.after), p.code(p.nextAt)))
	}
	return strings.Join(gaps, "; ")
}

// changes reports whether the fix changes anything.
func (p *fixPlan) changes() bool {
	if len(p.dropped) > 0 || p.delta != 0 || p.start != p.first {
		return true
	}
	for _, st := range p.steps {
		if st.rip || st.ep != st.oldEp {
			return true
		}
	}
	return false
}

func (s *session) printFix(p *fixPlan) {
	fmt.Fprintln(s.out, "\nThe fix:")
	for _, st := range p.steps {
		var what string
		switch {
		case st.rip && st.oldEp == 0:
			what = "rip now"
		case st.rip:
			what = fmt.Sprintf("rip now (nothing left of it as %s)", p.code(st.oldEp))
		case st.ep == st.oldEp:
			what = "unchanged"
		default:
			what = fmt.Sprintf("rename (was %s)", p.code(st.oldEp))
		}
		fmt.Fprintf(s.out, "    %s  title %-3d %8s  %s\n", p.code(st.ep), st.title.ID, titleLen(st.title), what)
	}
	for _, st := range p.dropped {
		fmt.Fprintf(s.out, "    delete  title %-3d %8s  (was %s): its rip, encoded file and queue entry\n",
			st.title.ID, titleLen(st.title), p.code(st.oldEp))
	}
	switch {
	case p.delta == 0:
		fmt.Fprintln(s.out, "    Later discs: no change")
	case len(p.later) == 0:
		fmt.Fprintf(s.out, "    Later discs: none yet; the next disc will start at %s\n", p.code(p.box.NextEpisode+p.delta))
	default:
		first, last := p.later[0].Episode, p.later[len(p.later)-1].Episode
		fmt.Fprintf(s.out, "    Later discs: %s–%s become %s–%s (rips, encoded files and queue entries renamed)\n",
			p.code(first), p.code(last), p.code(first+p.delta), p.code(last+p.delta))
	}
	fmt.Fprintln(s.out)
}

// fixOps is what applying a fix does to the files and the queue.
type fixOps struct {
	moves   [][2]string // from, to
	deletes []string
	encodes []store.Encode // entries to update (already changed)
	outputs map[int64]string
}

// collectOps works out every rename and deletion, checking that nothing
// is in the way and that no affected file is being encoded.
func (s *session) collectOps(ctx context.Context, p *fixPlan) (*fixOps, error) {
	ops := &fixOps{outputs: map[int64]string{}}
	dirs := s.episodeDirs(p)
	rename := func(from, to int) error {
		files, _, err := s.episodeFiles(ctx, p, from, dirs)
		if err != nil {
			return err
		}
		ob, nb := p.base(from), p.base(to)
		moved := map[string]string{}
		for _, f := range files {
			dst := filepath.Join(filepath.Dir(f), nb+strings.TrimPrefix(filepath.Base(f), ob))
			ops.moves = append(ops.moves, [2]string{f, dst})
			moved[f] = dst
		}
		es, err := s.store.EncodesForSource(ctx, p.ripPath(from))
		if err != nil {
			return err
		}
		for _, e := range es {
			if e.Status == store.EncodeEncoding {
				return fmt.Errorf("%s is being encoded; try again when it has finished", e.Title)
			}
			e.SourcePath, e.Title = p.ripPath(to), nb
			e.DestName = filepath.Join(filepath.Dir(e.DestName), nb)
			ops.encodes = append(ops.encodes, e)
			if dst, ok := moved[e.OutputPath]; ok {
				ops.outputs[e.ID] = dst
			}
		}
		return nil
	}
	drop := func(ep int, ripOnly bool) error {
		files, inRip, err := s.episodeFiles(ctx, p, ep, dirs)
		if err != nil {
			return err
		}
		for _, f := range files {
			if !ripOnly || inRip[f] {
				ops.deletes = append(ops.deletes, f)
			}
		}
		es, err := s.store.EncodesForSource(ctx, p.ripPath(ep))
		if err != nil {
			return err
		}
		for _, e := range es {
			switch {
			case e.Status == store.EncodeEncoding:
				return fmt.Errorf("%s is being encoded; try again when it has finished", e.Title)
			case ripOnly:
			case e.Status == store.EncodeWaiting || e.Status == store.EncodeFailed:
				e.Status = store.EncodeRemoved
				ops.encodes = append(ops.encodes, e)
			}
		}
		return nil
	}

	for _, st := range p.steps {
		switch {
		case st.rip && st.oldEp != 0:
			if err := drop(st.oldEp, true); err != nil { // replaced by the new rip
				return nil, err
			}
		case !st.rip && st.ep != st.oldEp:
			if err := rename(st.oldEp, st.ep); err != nil {
				return nil, err
			}
		}
	}
	for _, st := range p.dropped {
		if err := drop(st.oldEp, false); err != nil {
			return nil, err
		}
	}
	for _, e := range p.later {
		if err := rename(e.Episode, e.Episode+p.delta); err != nil {
			return nil, err
		}
	}

	// Nothing may be in the way: every destination is free, or is itself
	// moved away or deleted first.
	leaving := map[string]bool{}
	for _, m := range ops.moves {
		leaving[m[0]] = true
	}
	for _, f := range ops.deletes {
		leaving[f] = true
	}
	taken := map[string]bool{}
	check := func(dst string) error {
		if taken[dst] {
			return fmt.Errorf("two files would be renamed to %s", dst)
		}
		taken[dst] = true
		if _, err := os.Lstat(dst); err == nil && !leaving[dst] {
			return fmt.Errorf("%s is in the way", dst)
		}
		return nil
	}
	for _, m := range ops.moves {
		if err := check(m[1]); err != nil {
			return nil, err
		}
	}
	_, names := p.ripSteps()
	for _, n := range names {
		if err := check(filepath.Join(p.ripDir, n+".mkv")); err != nil {
			return nil, err
		}
	}
	return ops, nil
}

// apply renames and deletes the files: first every file to be renamed
// gets a temporary name (undone if one fails), then the deletions, then
// the final names, so swapped or shifted episodes never collide.
func (ops *fixOps) apply() error {
	temp := func(i int) string { return fmt.Sprintf("%s.blurip-fix-%d", ops.moves[i][0], i) }
	for i, m := range ops.moves {
		if err := os.Rename(m[0], temp(i)); err != nil {
			for j := i - 1; j >= 0; j-- {
				os.Rename(temp(j), ops.moves[j][0])
			}
			return fmt.Errorf("renaming %s: %w (nothing was changed)", m[0], err)
		}
	}
	for _, f := range ops.deletes {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("deleting %s: %w", f, err)
		}
	}
	var failed []string
	for i, m := range ops.moves {
		if err := os.Rename(temp(i), m[1]); err != nil {
			failed = append(failed, fmt.Sprintf("%s (should be %s)", temp(i), m[1]))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("couldn't give these files their new names: %s", strings.Join(failed, ", "))
	}
	return nil
}

// begin counts a disc of a season (its folder) as being ripped or waiting
// to be, until the returned function is called, so a fix never renumbers
// a season while another drive is ripping it.
func (bs *boxSetState) begin(folder string) func() {
	key := strings.ToLower(folder)
	bs.activeMu.Lock()
	if bs.active == nil {
		bs.active = map[string]int{}
	}
	bs.active[key]++
	bs.activeMu.Unlock()
	return func() {
		bs.activeMu.Lock()
		bs.active[key]--
		bs.activeMu.Unlock()
	}
}

// activeDiscs is how many discs of a season (its folder) are being
// ripped or waiting to be.
func (bs *boxSetState) activeDiscs(folder string) int {
	bs.activeMu.Lock()
	defer bs.activeMu.Unlock()
	return bs.active[strings.ToLower(folder)]
}

// executeFix rips what the fix needs, then renames and deletes, then
// records it all. If the rip fails, nothing else is changed.
func (s *session) executeFix(ctx context.Context, pr *plannedRip) error {
	dbctx := context.WithoutCancel(ctx)
	p, job, dev := pr.fix, pr.job, pr.dev
	start := s.now()
	label := p.box.Show + " " + boxset.Range(p.box.Season, p.start, len(p.steps))
	s.view.SetDisc(label, p.ripDir+string(filepath.Separator))
	var err error
	if job.ID, err = s.store.StartJob(dbctx, job); err != nil {
		return err
	}
	titles, names := p.ripSteps()
	for _, t := range titles {
		if err := s.store.AddJobTitle(dbctx, job.ID, t.ID, selectionMakeMKVDefault, selectionMakeMKVDefault); err != nil {
			return err
		}
	}
	fail := func(status, why string) error {
		job.FinishedAt, job.Status, job.FailureReason = s.now(), status, why
		job.RipTime = job.FinishedAt.Sub(start)
		if err := s.store.FinishJob(dbctx, job); err != nil {
			return err
		}
		if status == store.StatusInterrupted {
			s.view.Log("%s: interrupted; nothing was changed and the disc left in the drive", label)
			return errInterrupted
		}
		s.view.Log("✗ %s: %s", label, why)
		s.eject(ctx, dev)
		return errRipFailed
	}

	// Rip first, into a folder of its own, so a failed rip changes nothing.
	staging := filepath.Join(s.cfg.OutputDir, rip.TempDirName, fmt.Sprintf("fix-%d", job.ID))
	defer os.RemoveAll(staging)
	var res rip.Result
	if len(titles) > 0 {
		rips := s.rips
		if rips == nil {
			rips = &ripCounter{}
		}
		ripID := rips.start()
		res, err = rip.Run(ctx, rip.Request{
			MakeMKVCon: s.cfg.MakeMKVCon, Device: dev, Titles: titles,
			OutputDir: s.cfg.OutputDir, DestDir: staging, Name: label, JobID: fmt.Sprintf("job-%d", job.ID),
			FileNames: names, MaxFillPercent: s.cfg.MaxFillPercent, Reservations: s.reservations,
			OnProgress: s.view.Progress,
			OnTitle: func(i, n int, t makemkv.Title) {
				s.view.ProgressDone()
				s.view.Activity(fmt.Sprintf("%s (%d of %d, %s)", names[i-1], i, n, t.Duration()))
			},
		})
		job.RipsAtOnce = rips.finish(ripID)
		s.view.ProgressDone()
		switch {
		case ctx.Err() != nil:
			return fail(store.StatusInterrupted, "stopped with Ctrl-C")
		case err != nil:
			return fail(store.StatusFailed, err.Error()+"; nothing was renamed or deleted")
		}
	}

	// Then rename and delete, with no other disc of the season being
	// numbered meanwhile.
	moved, why, err := s.applyFix(dbctx, p, staging, res, job.ID)
	if err != nil {
		return err
	}
	if why != "" {
		return fail(store.StatusFailed, why)
	}

	job.FinishedAt, job.Status, job.OutputPath, job.BytesWritten = s.now(), store.StatusSucceeded, p.ripDir, res.Bytes
	job.RipTime = job.FinishedAt.Sub(start)
	job.Note = p.summary()
	if err := s.store.FinishJob(dbctx, job); err != nil {
		return fmt.Errorf("recording job %d: %w", job.ID, err)
	}
	s.view.Log("✓ %s: %s", label, job.Note)
	if s.queue && len(moved) > 0 {
		choice := pr.choice
		choice.Folder, choice.FileNames = p.folder, names
		s.queueEncodes(dbctx, pr, choice, rip.Result{Name: label, Dir: p.ripDir, Files: moved, Bytes: res.Bytes}, job.ID)
	}
	s.eject(ctx, dev)
	return nil
}

// applyFix renames and deletes the files, moves the new rips into place
// and updates the records, holding the box set lock. why says why it
// couldn't (the job fails); err is a database error.
func (s *session) applyFix(dbctx context.Context, p *fixPlan, staging string, res rip.Result, jobID int64) (moved []string, why string, err error) {
	bs := s.boxSet
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if p.delta != 0 && bs.activeDiscs(p.folder) > 1 {
		return nil, "another disc of this season is being ripped; put this one in again when it has finished (nothing was changed)", nil
	}
	s.view.Activity("Renaming...")
	ops, err := s.collectOps(dbctx, p)
	if err != nil {
		return nil, err.Error() + "; nothing was changed", nil
	}
	if err := ops.apply(); err != nil {
		return nil, err.Error(), nil
	}
	if len(res.Files) > 0 {
		if err := os.MkdirAll(p.ripDir, 0o755); err != nil {
			return nil, fmt.Sprintf("creating %s: %v", p.ripDir, err), nil
		}
	}
	for _, f := range res.Files {
		if err := os.Rename(filepath.Join(res.Dir, f), filepath.Join(p.ripDir, f)); err != nil {
			keep := filepath.Join(s.cfg.OutputDir, rip.TempDirName, fmt.Sprintf("job-%d-kept", jobID))
			os.Rename(staging, keep)
			os.WriteFile(filepath.Join(keep, rip.KeepMarker), []byte("ripped titles that could not be moved into place\n"), 0o644)
			return nil, fmt.Sprintf("moving %s into %s: %v; the new rips were kept in %s", f, p.ripDir, err, keep), nil
		}
		moved = append(moved, f)
	}

	// The records follow the files.
	for _, e := range ops.encodes {
		if err := s.store.UpdateEncode(dbctx, e); err != nil {
			return nil, "", err
		}
		if out, ok := ops.outputs[e.ID]; ok {
			if err := s.store.SetEncodeOutput(dbctx, e.ID, out); err != nil {
				return nil, "", err
			}
		}
	}
	eps := make([]store.Episode, len(p.steps))
	for i, st := range p.steps {
		eps[i] = store.Episode{TitleID: st.title.ID, Episode: st.ep}
	}
	if err := s.store.RenumberDisc(dbctx, p.box.ID, p.discID, p.discNo, eps, p.after, p.delta, s.now()); err != nil {
		return nil, "", err
	}
	return moved, "", nil
}

// summary says what the fix did, for the log and the history.
func (p *fixPlan) summary() string {
	if p.whole {
		return fmt.Sprintf("ripped again as %s", boxset.Range(p.box.Season, p.first, len(p.steps)))
	}
	var parts []string
	for _, st := range p.steps {
		switch {
		case st.rip:
			parts = append(parts, fmt.Sprintf("title %d ripped as %s", st.title.ID, p.code(st.ep)))
		case st.ep != st.oldEp:
			parts = append(parts, fmt.Sprintf("%s renamed %s", p.code(st.oldEp), p.code(st.ep)))
		}
	}
	for _, st := range p.dropped {
		parts = append(parts, fmt.Sprintf("%s (title %d) deleted", p.code(st.oldEp), st.title.ID))
	}
	if len(p.later) > 0 {
		first, last := p.later[0].Episode, p.later[len(p.later)-1].Episode
		parts = append(parts, fmt.Sprintf("later episodes %s–%s renumbered %s–%s",
			p.code(first), p.code(last), p.code(first+p.delta), p.code(last+p.delta)))
	}
	return "fixed: " + strings.Join(parts, "; ")
}

// runBoxSetFix is `blurip boxset fix SHOW SEASON [DISC]`: fixing a disc
// without it in the drive. Its titles can be put in another order, or
// left out (deleted), with the later episodes renumbered to match; a
// title that has to be ripped needs the disc (blurip -B).
func runBoxSetFix(ctx context.Context, dbPath string, args []string) error {
	return boxSetFix(ctx, dbPath, args, os.Stdin, os.Stdout, isTerminal(os.Stdout), isTerminal(os.Stdin))
}

func boxSetFix(ctx context.Context, dbPath string, args []string, in io.Reader, out io.Writer, tty, canAsk bool) error {
	if len(args) < 2 || len(args) > 3 {
		return errors.New("usage: blurip boxset fix SHOW SEASON [DISC]")
	}
	season, err := strconv.Atoi(args[1])
	if err != nil {
		return errors.New("SEASON must be a number")
	}
	discNo := -1
	if len(args) == 3 {
		if discNo, err = strconv.Atoi(args[2]); err != nil {
			return errors.New("DISC must be a number")
		}
	}
	lock, err := store.AcquireLock(dbPath)
	if err != nil {
		return fmt.Errorf("%w (stop blurip before fixing a box set)", err)
	}
	defer lock.Release()
	st, cfg, err := openStore(ctx, dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	b, ok, err := st.FindBoxSet(ctx, args[0], season)
	if err != nil {
		return err
	}
	if !ok {
		return boxSetErr(fmt.Errorf("%w: %s season %d", store.ErrUnknownBoxSet, args[0], season))
	}
	discs, err := st.BoxSetDiscs(ctx, b.ID)
	if err != nil {
		return err
	}
	ask := prompt.NewPrompter(in, out).WithContext(ctx)
	if discNo < 0 {
		what, err := ask.Choice(fmt.Sprintf("What needs fixing in %s season %d?", b.Show, b.Season), []prompt.Option{
			{Value: "disc", Label: "A disc's episodes: their order, an extra, a missed episode, a gap in the numbering"},
			{Value: "rename", Label: "The show's name or the season number (every disc of it)"},
		}, "disc")
		if err != nil {
			return err
		}
		if what == "rename" {
			s := &session{cfg: cfg, store: st, ask: ask, out: out, now: time.Now, boxSet: &boxSetState{}, canAsk: canAsk}
			done, err := s.askSeasonRename(ctx, b)
			if done != "" {
				fmt.Fprintln(out, done)
			}
			return err
		}
	}
	disc, err := pickBoxSetDisc(ask, b, discs, discNo)
	if err != nil {
		return err
	}

	d, _, err := st.EpisodesForDisc(ctx, disc.DiscID)
	if err != nil {
		return err
	}
	ripDir := filepath.Join(cfg.DestDir(makemkv.KindOf(disc.Type)), b.Show, boxset.SeasonFolder(b.Season))
	if prev, ok, err := st.LastSuccess(ctx, disc.DiscID); err != nil {
		return err
	} else if ok && prev.OutputPath != "" {
		ripDir = prev.OutputPath
	}
	byID := map[int]makemkv.Title{}
	var titles []makemkv.Title
	for _, e := range d.Episodes {
		t := makemkv.Title{ID: e.TitleID}
		byID[e.TitleID] = t
		titles = append(titles, t)
	}
	p, order, oldEp, ok := newFixPlan(d, disc.DiscID, ripDir, byID)
	if !ok {
		return fmt.Errorf("%s's episodes don't run on from one another, so it can't be fixed here", dash(disc.Name))
	}

	s := &session{cfg: cfg, store: st, ask: ask, out: out, now: time.Now, boxSet: &boxSetState{}, canAsk: canAsk}
	fmt.Fprintf(out, "\n%s: %s %s\n", dash(disc.Name), b.Show, boxset.Range(b.Season, p.first, len(order)))
	if err := s.printEpisodes(ctx, p, titles, oldEp); err != nil {
		return err
	}
	needsDisc := func(p *fixPlan) error {
		for _, st := range p.steps {
			if st.rip {
				return fmt.Errorf("title %d has no rip or encoded file left, so it has to be ripped: put the disc in and run blurip -B (choose Fix it)", st.title.ID)
			}
		}
		return nil
	}
	ok, why, err := s.askFix(ctx, p, order, byID, oldEp, needsDisc)
	if err != nil {
		return err
	}
	if !ok {
		if why == "fixing it was cancelled" {
			fmt.Fprintln(out, "Nothing was changed.")
		}
		return nil
	}

	board := newBoard(out, tty)
	defer board.Close()
	s.view = board.Row("fix", "Fix")
	job := store.Job{DiscID: disc.DiscID, StartedAt: s.now()}
	_, why, err = s.applyFix(context.WithoutCancel(ctx), p, "", rip.Result{}, 0)
	if err != nil {
		return err
	}
	s.view.Idle()
	job.FinishedAt = s.now()
	if why != "" {
		job.Status, job.FailureReason = store.StatusFailed, why
	} else {
		job.Status, job.OutputPath, job.Note = store.StatusSucceeded, p.ripDir, p.summary()
	}
	if _, err := st.RecordJob(context.WithoutCancel(ctx), job); err != nil {
		return err
	}
	if why != "" {
		return errors.New(why)
	}
	board.Logf("✓ %s %s: %s", b.Show, boxset.Range(b.Season, p.start, len(p.steps)), job.Note)
	return nil
}

// pickBoxSetDisc finds disc number discNo of a box set, or (-1) asks which.
func pickBoxSetDisc(ask *prompt.Prompter, b store.BoxSet, discs []store.BoxSetDisc, discNo int) (store.BoxSetDisc, error) {
	describe := func(d store.BoxSetDisc) string {
		return fmt.Sprintf("%s: %s (%s)", discWord(d.DiscNo), boxset.Range(b.Season, d.First, d.Episodes), dash(d.Name))
	}
	if len(discs) == 0 {
		return store.BoxSetDisc{}, fmt.Errorf("%s season %d has no numbered discs", b.Show, b.Season)
	}
	if discNo >= 0 {
		var found []store.BoxSetDisc
		for _, d := range discs {
			if d.DiscNo == discNo {
				found = append(found, d)
			}
		}
		if len(found) == 1 {
			return found[0], nil
		}
		var list []string
		for _, d := range discs {
			list = append(list, "  "+describe(d))
		}
		what := "no"
		if len(found) > 1 {
			what = "more than one"
		}
		return store.BoxSetDisc{}, fmt.Errorf("%s season %d has %s disc %d; leave the disc number out to choose from:\n%s",
			b.Show, b.Season, what, discNo, strings.Join(list, "\n"))
	}
	if len(discs) == 1 {
		return discs[0], nil
	}
	opts := make([]prompt.Option, len(discs))
	for i, d := range discs {
		opts[i] = prompt.Option{Value: strconv.Itoa(i), Label: describe(d)}
	}
	v, err := ask.Choice(fmt.Sprintf("Which disc of %s season %d?", b.Show, b.Season), opts, "")
	if err != nil {
		return store.BoxSetDisc{}, err
	}
	i, _ := strconv.Atoi(v)
	return discs[i], nil
}
