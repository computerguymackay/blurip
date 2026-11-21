package boxset

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/computerguymackay/blurip/internal/makemkv"
)

// Some discs put a season's episodes into one long title, with chapters
// marking where each starts. Such a title is ripped as one file and split
// into episodes when it is encoded: HandBrake encodes one chapter range
// per episode.

// Chapters is a range of a title's chapters, numbered from 1.
type Chapters struct{ First, Last int }

func (c Chapters) String() string {
	if c.First == c.Last {
		return strconv.Itoa(c.First)
	}
	return fmt.Sprintf("%d-%d", c.First, c.Last)
}

// FormatChapters is "1-5,6-10,11-15".
func FormatChapters(cs []Chapters) string {
	s := make([]string, len(cs))
	for i, c := range cs {
		s[i] = c.String()
	}
	return strings.Join(s, ",")
}

// ParseChapters reads "1-5, 6-10, 11" (one range per episode). The
// ranges must follow on from one another, start at chapter 1 and stay
// within total chapters (total 0: not checked). "" is no ranges.
func ParseChapters(s string, total int) ([]Chapters, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var out []Chapters
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		c, err := ParseRange(part)
		if err != nil {
			return nil, err
		}
		want := 1
		if len(out) > 0 {
			want = out[len(out)-1].Last + 1
		}
		if c.First != want {
			return nil, fmt.Errorf("%s should start at chapter %d: each episode carries on where the one before ended", part, want)
		}
		out = append(out, c)
	}
	if total > 0 && out[len(out)-1].Last > total {
		return nil, fmt.Errorf("the title has only %d chapters", total)
	}
	return out, nil
}

// ParseRange reads one chapter range: "6-10" or "7".
func ParseRange(s string) (Chapters, error) {
	var c Chapters
	a, b, isRange := strings.Cut(strings.TrimSpace(s), "-")
	var err1, err2 error
	c.First, err1 = strconv.Atoi(a)
	c.Last = c.First
	if isRange {
		c.Last, err2 = strconv.Atoi(b)
	}
	if err1 != nil || err2 != nil || c.First < 1 || c.Last < c.First {
		return Chapters{}, fmt.Errorf("%q isn't a chapter range like 1-5", s)
	}
	return c, nil
}

// SplitEvenly shares chapters out between episodes, in order. If they
// don't divide evenly, the first episodes get one more each.
func SplitEvenly(chapters, episodes int) []Chapters {
	if episodes < 1 || chapters < episodes {
		return nil
	}
	out := make([]Chapters, episodes)
	next := 1
	for i := range out {
		n := chapters / episodes
		if i < chapters%episodes {
			n++
		}
		out[i] = Chapters{next, next + n - 1}
		next += n
	}
	return out
}

// Episode lengths TV is usually made in, used to guess how many episodes
// a long title holds.
var usualLengths = []time.Duration{22 * time.Minute, 25 * time.Minute, 30 * time.Minute, 45 * time.Minute, 50 * time.Minute, 60 * time.Minute}

// GuessEpisodes guesses how many episodes a title of length d with n
// chapters holds: the count whose episodes come closest to a usual
// episode length, preferring counts that share the chapters out evenly.
// 0 means no good guess.
func GuessEpisodes(d time.Duration, chapters int) int {
	best, bestScore := 0, math.Inf(1)
	for n := 2; n <= chapters; n++ {
		ep := d / time.Duration(n)
		if ep < 18*time.Minute {
			break
		}
		score := math.Inf(1)
		for _, u := range usualLengths {
			score = min(score, math.Abs(float64(ep-u))/float64(u))
		}
		if chapters%n != 0 {
			score += 0.1
		}
		if score < bestScore {
			best, bestScore = n, score
		}
	}
	if bestScore > 0.25 {
		return 0
	}
	return best
}

// MinSplit is the shortest title offered to be split into episodes.
const MinSplit = 40 * time.Minute

// MightHoldEpisodes reports whether a title could be several episodes in
// one: long, with enough chapters to mark them.
func MightHoldEpisodes(t makemkv.Title) bool {
	return t.Duration() >= MinSplit && t.Chapters() >= 4
}

// SplitCandidate returns the title of a disc that is probably a season's
// episodes in one: the only episode found, long and with chapters.
func SplitCandidate(sel Selection) (makemkv.Title, bool) {
	if len(sel.Episodes) != 1 || !MightHoldEpisodes(sel.Episodes[0]) {
		return makemkv.Title{}, false
	}
	return sel.Episodes[0], true
}

// FileNameRange is "Spaced - S01E01-E07", the name of a file holding
// episodes first to first+n-1 (as Plex names multi-episode files).
func FileNameRange(show string, season, first, n int) string {
	if n <= 1 {
		return FileName(show, season, first)
	}
	return fmt.Sprintf("%s-E%02d", FileName(show, season, first), first+n-1)
}
