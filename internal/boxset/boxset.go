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
	Show       string // "Friends"; "" if the label has no recognisable show name
	Season     int
	LastSeason int // for a disc labelled with two seasons ("SEASON6AND7"): 7; otherwise 0
	Disc       int
	From       string // the text it was read from
}

var (
	// "S1D2", "S01 D01", "SEASON 1 DISC 2", "SERIES 3 DISK 1", "S2 DVD1"
	reSeasonDisc = regexp.MustCompile(`\b(SEASONS?|SERIES|S)\s*0*(\d{1,2})\s*(?:DISC|DISK|DVD|BD|D)\s*0*(\d{1,2})\b`)
	reSeason     = regexp.MustCompile(`\b(SEASONS?|SERIES|S)\s*0*(\d{1,2})\b`)
	reDisc       = regexp.MustCompile(`\b(?:DISC|DISK|DVD|BD|D)\s*0*(\d{1,2})\b`)
	// A second season after the first: "SEASON 6 AND 7", "SEASONS 1 2" (from "1-2").
	reAndSeason = regexp.MustCompile(`^\s+(?:AND|TO)\s+0*(\d{1,2})\b`)
	rePlainNext = regexp.MustCompile(`^\s+0*(\d{1,2})\b`)
	// Words that often sit between the show name and the season.
	reFiller = regexp.MustCompile(`\b(?:THE\s+)?(?:COMPLETE|FULL|FINAL|BOX\s*SET)\s*$`)
	// Letters and digits run together: "SEASON6AND7", "S1D1", "UPB1".
	reLetterDigit = regexp.MustCompile(`([A-Z])(\d)`)
	reDigitLetter = regexp.MustCompile(`(\d)([A-Z])`)
	// Numbers as words: "SEASON ONE", "DISC TWO", "THE COMPLETE FIRST SERIES".
	reWordNumber = regexp.MustCompile(`\b(SEASONS?|SERIES|DISC|DISK|VOLUME|AND|TO)\s+(ONE|TWO|THREE|FOUR|FIVE|SIX|SEVEN|EIGHT|NINE|TEN|ELEVEN|TWELVE|THIRTEEN|FOURTEEN|FIFTEEN|SIXTEEN|SEVENTEEN|EIGHTEEN|NINETEEN|TWENTY)\b`)
	reOrdinal    = regexp.MustCompile(`\b(FIRST|SECOND|THIRD|FOURTH|FIFTH|SIXTH|SEVENTH|EIGHTH|NINTH|TENTH|ELEVENTH|TWELFTH)\s+(SEASON|SERIES)\b`)
)

var numberWords = map[string]int{
	"ONE": 1, "TWO": 2, "THREE": 3, "FOUR": 4, "FIVE": 5, "SIX": 6, "SEVEN": 7, "EIGHT": 8, "NINE": 9, "TEN": 10,
	"ELEVEN": 11, "TWELVE": 12, "THIRTEEN": 13, "FOURTEEN": 14, "FIFTEEN": 15, "SIXTEEN": 16, "SEVENTEEN": 17,
	"EIGHTEEN": 18, "NINETEEN": 19, "TWENTY": 20,
	"FIRST": 1, "SECOND": 2, "THIRD": 3, "FOURTH": 4, "FIFTH": 5, "SIXTH": 6, "SEVENTH": 7, "EIGHTH": 8,
	"NINTH": 9, "TENTH": 10, "ELEVENTH": 11, "TWELFTH": 12,
}

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
	norm = reLetterDigit.ReplaceAllString(norm, "$1 $2")
	norm = reDigitLetter.ReplaceAllString(norm, "$1 $2")
	norm = reWordNumber.ReplaceAllStringFunc(norm, func(m string) string {
		f := strings.Fields(m)
		return fmt.Sprintf("%s %d", f[0], numberWords[f[1]])
	})
	norm = reOrdinal.ReplaceAllStringFunc(norm, func(m string) string {
		f := strings.Fields(m)
		return fmt.Sprintf("%s %d", f[1], numberWords[f[0]])
	})
	if norm == "" {
		return Label{}
	}
	l := Label{From: s}
	cut, seasonEnd, keyword := -1, -1, ""
	if m := reSeasonDisc.FindStringSubmatchIndex(norm); m != nil {
		keyword = norm[m[2]:m[3]]
		l.Season, _ = strconv.Atoi(norm[m[4]:m[5]])
		l.Disc, _ = strconv.Atoi(norm[m[6]:m[7]])
		cut = m[0]
	} else {
		if m := reSeason.FindStringSubmatchIndex(norm); m != nil {
			keyword = norm[m[2]:m[3]]
			l.Season, _ = strconv.Atoi(norm[m[4]:m[5]])
			cut, seasonEnd = m[0], m[1]
		}
		if m := reDisc.FindStringSubmatchIndex(norm); m != nil && m[0] != cut {
			l.Disc, _ = strconv.Atoi(norm[m[2]:m[3]])
			if cut < 0 || m[0] < cut {
				cut = m[0]
			}
		}
	}
	if cut < 0 {
		return Label{} // nothing box-set-like here
	}
	if seasonEnd >= 0 && keyword != "S" {
		rest := norm[seasonEnd:]
		m := reAndSeason.FindStringSubmatch(rest)
		if m == nil && keyword == "SEASONS" {
			m = rePlainNext.FindStringSubmatch(rest)
		}
		if m != nil {
			if n, _ := strconv.Atoi(m[1]); n > l.Season {
				l.LastSeason = n
			}
		}
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
	tolerance  = 0.15             // most episodes are within 15% of each other's length
	// Titles shorter than this share of the usual episode length are
	// extras; between it and the usual length they are kept but flagged
	// (Columbo's 70- and 90-minute episodes share discs: 74/95 ≈ 0.78).
	minShare = 0.6
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
	// Longer lists the episodes much longer than the usual length (not
	// double episodes): kept, but on a disc that isn't a box set they
	// would be a film's main feature.
	Longer []int
	// Shorter lists the episodes kept although shorter than the usual
	// length (they could be extras).
	Shorter []int
}

// MaxTVEpisode: titles this long or longer are films, not TV episodes
// (even feature-length episodes such as Columbo's run about 70–95 minutes).
const MaxTVEpisode = 100 * time.Minute

// LooksLikeTV reports whether a disc's titles look like TV episodes: at
// least two of about the same length, each under MaxTVEpisode, and none
// much longer (which would be a film's main feature). Disc labels can't be
// trusted for this: "S1M0NE" and "SERIES_7_THE_CONTENDERS" are films.
func LooksLikeTV(sel Selection) bool {
	if len(sel.Episodes)-len(sel.Shorter) < 2 { // titles of an unusual length don't count
		return false
	}
	if len(sel.Episodes) < 2 {
		return false
	}
	for _, t := range sel.Episodes {
		if t.Duration() >= MaxTVEpisode {
			return false
		}
	}
	return len(sel.Longer) == 0
}

// Episodes picks the episodes from a disc's titles, leaving out short
// extras, "play all" titles and duplicates. Titles of an unusual length
// are kept but flagged, since every disc's list is checked before ripping
// and some shows mix episode lengths.
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
		case r < minShare:
			sel.Left = append(sel.Left, Left{t, "much shorter than the episodes (extra)"})
		case r < 1:
			sel.Episodes = append(sel.Episodes, t)
			sel.Shorter = append(sel.Shorter, t.ID)
			sel.Warnings = append(sel.Warnings, fmt.Sprintf("title %d (%s) is shorter than most episodes (%s): check it's an episode, not an extra",
				t.ID, t.Duration(), ref.Round(time.Second)))
		default:
			sel.Episodes = append(sel.Episodes, t)
			sel.Longer = append(sel.Longer, t.ID)
			sel.Warnings = append(sel.Warnings, fmt.Sprintf("title %d (%s) is longer than most episodes (%s): check it's an episode, not a special or film",
				t.ID, t.Duration(), ref.Round(time.Second)))
		}
	}
	if len(sel.Episodes) == 1 {
		sel.Warnings = append(sel.Warnings, "only one episode found on this disc")
	}
	sort.Slice(sel.Left, func(i, j int) bool { return sel.Left[i].Title.ID < sel.Left[j].Title.ID })
	return sel
}

// sameContent reports two titles that are the same video: the same length
// (within 2s) and either the same segment map, if MakeMKV gives one, or
// the same size. A segment map alone isn't enough: on DVDs it lists cells,
// which are numbered from 1 in each title set, so two different episodes
// can both be "1-12".
func sameContent(a, b makemkv.Title) bool {
	d := a.Duration() - b.Duration()
	if d <= -2*time.Second || d >= 2*time.Second {
		return false
	}
	sa, sb := a.Attrs[makemkv.AttrSegmentsMap], b.Attrs[makemkv.AttrSegmentsMap]
	if sa != "" && sb != "" {
		return sa == sb
	}
	return a.SizeBytes() == b.SizeBytes() && a.SizeBytes() > 0
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
