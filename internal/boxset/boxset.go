// Package boxset handles TV box sets: reading the show, season and disc
// number from a disc's label, picking the episodes out of a disc's titles,
// and naming them the way Plex expects (Show/Season 01/Show - S01E01.mkv).
package boxset

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/computerguymackay/blurip/internal/makemkv"
)

// Label is what a disc's name says about where it sits in a box set. Zero
// values mean "not found".
type Label struct {
	Show   string // "Friends"; "" if the label has no recognisable show name
	Season int
	Disc   int
	From   string // the text it was read from
}

var (
	// "S1D2", "S01 D01", "SEASON 1 DISC 2", "SERIES 3 DISK 1", "S2 DVD1"
	reSeasonDisc = regexp.MustCompile(`\b(?:SEASON|SERIES|S)\s*0*(\d{1,2})\s*(?:DISC|DISK|DVD|BD|D)\s*0*(\d{1,2})\b`)
	reSeason     = regexp.MustCompile(`\b(?:SEASON|SERIES|S)\s*0*(\d{1,2})\b`)
	reDisc       = regexp.MustCompile(`\b(?:DISC|DISK|DVD|BD|D)\s*0*(\d{1,2})\b`)
	// Words that often sit between the show name and the season.
	reFiller = regexp.MustCompile(`\b(?:THE\s+)?(?:COMPLETE|FULL|FIRST|SECOND|THIRD|FINAL|BOX\s*SET)\s*$`)
	reJoined = regexp.MustCompile(`(SEASON|SERIES|DISC|DISK)(\d)`)
)

// ParseLabel reads show, season and disc from MakeMKV's disc name or the
// volume label, whichever says more. The name is tried first because it is
// usually nicer ("Friends: Season 1" rather than "FRIENDS_S1_D2").
func ParseLabel(discName, volumeLabel string) Label {
	best := Label{}
	for _, s := range []string{discName, volumeLabel} {
		l := parse(s)
		if score(l) > score(best) {
			best = l
		}
	}
	return best
}

func score(l Label) int {
	n := 0
	if l.Season > 0 {
		n += 2
	}
	if l.Disc > 0 {
		n += 2
	}
	if l.Show != "" {
		n++
	}
	return n
}

func parse(s string) Label {
	norm := strings.ToUpper(strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	}), " "))
	// Split letters from digits where labels run them together: "S1D2" is
	// handled by the patterns, but "SEASON1" and "DISC2" need a space.
	norm = reJoined.ReplaceAllString(norm, "$1 $2")
	if norm == "" {
		return Label{}
	}
	l := Label{From: s}
	cut := -1
	if m := reSeasonDisc.FindStringSubmatchIndex(norm); m != nil {
		l.Season, _ = strconv.Atoi(norm[m[2]:m[3]])
		l.Disc, _ = strconv.Atoi(norm[m[4]:m[5]])
		cut = m[0]
	} else {
		if m := reSeason.FindStringSubmatchIndex(norm); m != nil {
			l.Season, _ = strconv.Atoi(norm[m[2]:m[3]])
			cut = m[0]
		}
		if m := reDisc.FindStringSubmatchIndex(norm); m != nil {
			l.Disc, _ = strconv.Atoi(norm[m[2]:m[3]])
			if cut < 0 || m[0] < cut {
				cut = m[0]
			}
		}
	}
	if cut < 0 {
		return Label{} // nothing box-set-like here
	}
	show := strings.TrimSpace(reFiller.ReplaceAllString(strings.TrimSpace(norm[:cut]), ""))
	l.Show = titleCase(show)
	return l
}

func titleCase(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

// Detection limits.
const (
	MinEpisode = 10 * time.Minute // shorter titles are extras
	tolerance  = 0.15             // episodes are within 15% of each other's length
)

// Left is a title that was not picked as an episode, and why.
type Left struct {
	Title  makemkv.Title
	Reason string
}

// Selection is the result of picking episodes from a disc.
type Selection struct {
	Episodes []makemkv.Title // in disc order
	Left     []Left
	Warnings []string // things worth a human look before ripping
}

// Episodes picks the episodes from a disc's titles: titles of similar
// length, leaving out short extras, "play all" titles and duplicates.
func Episodes(titles []makemkv.Title) Selection {
	var sel Selection
	var cands []makemkv.Title
	for _, t := range titles {
		if t.Duration() < MinEpisode {
			sel.Left = append(sel.Left, Left{t, "too short (extra)"})
		} else {
			cands = append(cands, t)
		}
	}

	// Duplicates: the same content under two titles (Blu-ray playlists).
	var uniq []makemkv.Title
	for _, t := range cands {
		dup := -1
		for _, u := range uniq {
			if sameContent(t, u) {
				dup = u.ID
				break
			}
		}
		if dup >= 0 {
			sel.Left = append(sel.Left, Left{t, fmt.Sprintf("same as title %d", dup)})
		} else {
			uniq = append(uniq, t)
		}
	}
	cands = uniq

	// "Play all": a title as long as several others put together. With
	// only two, it could as well be a double-length episode, so it is
	// left out but flagged.
	for {
		i, n := playAll(cands)
		if i < 0 {
			break
		}
		sel.Left = append(sel.Left, Left{cands[i], fmt.Sprintf("all %d episodes in one (\"play all\")", n)})
		if n == 2 {
			sel.Warnings = append(sel.Warnings, fmt.Sprintf("title %d (%s) is as long as two episodes together: left out as \"play all\", but it could be a double episode",
				cands[i].ID, cands[i].Duration()))
		}
		cands = append(cands[:i:i], cands[i+1:]...)
	}
	if len(cands) == 0 {
		sel.Warnings = append(sel.Warnings, "no episodes found: every title is short (extras only?)")
		return sel
	}

	// The episodes are the biggest group of titles of similar length.
	ref := typicalLength(cands)
	for _, t := range cands {
		r := float64(t.Duration()) / float64(ref)
		switch {
		case r >= 1-tolerance && r <= 1+tolerance:
			sel.Episodes = append(sel.Episodes, t)
		case r >= 1.7 && r <= 2.3:
			// Double-length pilots and finales are common.
			sel.Episodes = append(sel.Episodes, t)
			sel.Warnings = append(sel.Warnings, fmt.Sprintf("title %d (%s) is about twice as long as the others: a double episode?", t.ID, t.Duration()))
		case r < 1:
			sel.Left = append(sel.Left, Left{t, "shorter than the episodes (extra)"})
		default:
			sel.Left = append(sel.Left, Left{t, "longer than the episodes (special or film?)"})
			sel.Warnings = append(sel.Warnings, fmt.Sprintf("title %d (%s) is much longer than the episodes and was left out", t.ID, t.Duration()))
		}
	}
	if len(sel.Episodes) == 1 {
		sel.Warnings = append(sel.Warnings, "only one episode found on this disc")
	}
	sort.Slice(sel.Left, func(i, j int) bool { return sel.Left[i].Title.ID < sel.Left[j].Title.ID })
	return sel
}

// sameContent reports two titles that are the same video: the same segment
// map if MakeMKV gives one, otherwise the same length and size.
func sameContent(a, b makemkv.Title) bool {
	sa, sb := a.Attrs[makemkv.AttrSegmentsMap], b.Attrs[makemkv.AttrSegmentsMap]
	if sa != "" && sb != "" {
		return sa == sb
	}
	d := a.Duration() - b.Duration()
	return d > -2*time.Second && d < 2*time.Second && a.SizeBytes() == b.SizeBytes() && a.SizeBytes() > 0
}

// playAll returns the index of a title whose length is the sum of at least
// two other titles (within 2%), and how many, or -1.
func playAll(ts []makemkv.Title) (int, int) {
	for i, t := range ts {
		var others []time.Duration
		for j, u := range ts {
			if j != i && u.Duration() < t.Duration() {
				others = append(others, u.Duration())
			}
		}
		if len(others) < 2 {
			continue
		}
		// Try the whole set first, then the largest similar-length group
		// (the play-all may leave out a bonus title).
		for _, set := range [][]time.Duration{others, similar(others)} {
			if len(set) < 2 {
				continue
			}
			var sum time.Duration
			for _, d := range set {
				sum += d
			}
			if diff := float64(t.Duration()-sum) / float64(sum); diff > -0.02 && diff < 0.02 {
				return i, len(set)
			}
		}
	}
	return -1, 0
}

// similar returns the biggest group of durations within tolerance of one another's anchor.
func similar(ds []time.Duration) []time.Duration {
	var best []time.Duration
	for _, a := range ds {
		var g []time.Duration
		for _, d := range ds {
			if r := float64(d) / float64(a); r >= 1-tolerance && r <= 1+tolerance {
				g = append(g, d)
			}
		}
		if len(g) > len(best) {
			best = g
		}
	}
	return best
}

// typicalLength is the median length of the biggest group of titles of
// similar length; ties go to the longer group.
func typicalLength(ts []makemkv.Title) time.Duration {
	var best []time.Duration
	var bestAnchor time.Duration
	for _, a := range ts {
		var g []time.Duration
		for _, t := range ts {
			if r := float64(t.Duration()) / float64(a.Duration()); r >= 1-tolerance && r <= 1+tolerance {
				g = append(g, t.Duration())
			}
		}
		if len(g) > len(best) || (len(g) == len(best) && a.Duration() > bestAnchor) {
			best, bestAnchor = g, a.Duration()
		}
	}
	sort.Slice(best, func(i, j int) bool { return best[i] < best[j] })
	return best[len(best)/2]
}

// Code is the episode code: S01E07.
func Code(season, episode int) string { return fmt.Sprintf("S%02dE%02d", season, episode) }

// Range is "S01E07–S01E12", or just "S01E07" for one episode.
func Range(season, first, n int) string {
	if n <= 1 {
		return Code(season, first)
	}
	return Code(season, first) + "–" + Code(season, first+n-1)
}

// SeasonFolder is "Season 01".
func SeasonFolder(season int) string { return fmt.Sprintf("Season %02d", season) }

// FileName is "Friends - S01E07" (without .mkv).
func FileName(show string, season, episode int) string {
	return show + " - " + Code(season, episode)
}
