package boxset

import (
	"testing"
	"time"

	"github.com/computerguymackay/blurip/internal/makemkv"
)

func TestParseChapters(t *testing.T) {
	cs, err := ParseChapters(" 1-5, 6-10,11 12-35", 35)
	if err != nil || FormatChapters(cs) != "1-5,6-10,11,12-35" {
		t.Errorf("got %v, %v", cs, err)
	}
	if cs, err := ParseChapters("", 35); err != nil || cs != nil {
		t.Errorf("empty: %v, %v", cs, err)
	}
	for _, bad := range []string{"2-5", "1-5,7-10", "1-5,6-40", "5-1", "a-b", "0-3"} {
		if _, err := ParseChapters(bad, 35); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSplitEvenly(t *testing.T) {
	if got := FormatChapters(SplitEvenly(35, 7)); got != "1-5,6-10,11-15,16-20,21-25,26-30,31-35" {
		t.Errorf("35/7 = %s", got)
	}
	// Not even: the first episodes get one more.
	if got := FormatChapters(SplitEvenly(11, 3)); got != "1-4,5-8,9-11" {
		t.Errorf("11/3 = %s", got)
	}
	if SplitEvenly(3, 4) != nil || SplitEvenly(3, 0) != nil {
		t.Error("impossible split made")
	}
}

func TestGuessEpisodes(t *testing.T) {
	for _, c := range []struct {
		d        time.Duration
		chapters int
		want     int
	}{
		{2*time.Hour + 50*time.Minute + 28*time.Second, 35, 7}, // Spaced: 7 × 24m, 5 chapters each
		{135 * time.Minute, 12, 3},                             // 3 × 45m
		{88 * time.Minute, 8, 4},                               // 4 × 22m
		{50 * time.Minute, 4, 2},                               // 2 × 25m
	} {
		if got := GuessEpisodes(c.d, c.chapters); got != c.want {
			t.Errorf("GuessEpisodes(%s, %d) = %d, want %d", c.d, c.chapters, got, c.want)
		}
	}
}

func TestSplitCandidate(t *testing.T) {
	title := func(id int, dur string, chapters string) makemkv.Title {
		return makemkv.Title{ID: id, Attrs: makemkv.Attrs{makemkv.AttrDuration: dur, makemkv.AttrChapterCount: chapters}}
	}
	sel := Episodes([]makemkv.Title{title(0, "0:14:51", "11"), title(1, "0:03:09", "6"), title(5, "2:50:28", "35")})
	if c, ok := SplitCandidate(sel); !ok || c.ID != 5 {
		t.Errorf("candidate = %v, %v (%+v)", c.ID, ok, sel)
	}
	// Episodes of their own: nothing to split.
	sel = Episodes([]makemkv.Title{title(1, "0:22:10", "6"), title(2, "0:22:40", "6"), title(3, "0:22:20", "6")})
	if _, ok := SplitCandidate(sel); ok {
		t.Error("split offered for a disc of episodes")
	}
	// Long, but no chapters to split it by.
	sel = Episodes([]makemkv.Title{title(5, "2:50:28", "1")})
	if _, ok := SplitCandidate(sel); ok {
		t.Error("split offered without chapters")
	}
}

func TestFileNameRange(t *testing.T) {
	if got := FileNameRange("Spaced", 1, 1, 7); got != "Spaced - S01E01-E07" {
		t.Error(got)
	}
	if got := FileNameRange("Spaced", 2, 8, 1); got != "Spaced - S02E08" {
		t.Error(got)
	}
}
