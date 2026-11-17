package boxset

import (
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/computerguymackay/blurip/internal/makemkv"
)

func TestParseLabel(t *testing.T) {
	for _, c := range []struct {
		name, label string
		want        Label
	}{
		{"", "FRIENDS_S1_D2", Label{Show: "Friends", Season: 1, Disc: 2}},
		{"", "SOPRANOS_S01D03", Label{Show: "Sopranos", Season: 1, Disc: 3}},
		{"", "THE_WIRE_SEASON_1_DISC_2", Label{Show: "The Wire", Season: 1, Disc: 2}},
		{"", "BBT_SEASON3_DISC1", Label{Show: "Bbt", Season: 3, Disc: 1}},
		{"", "BLACKADDER_SERIES_2_DISK_1", Label{Show: "Blackadder", Season: 2, Disc: 1}},
		{"", "LOST_COMPLETE_S2_D4", Label{Show: "Lost", Season: 2, Disc: 4}},
		{"Friends: Season 1", "FRIENDS_S1_D2", Label{Show: "Friends", Season: 1, Disc: 2}},
		{"Mad Men Season 4", "MADMEN4", Label{Show: "Mad Men", Season: 4}},
		{"", "BOX_SET_DISC_4", Label{Disc: 4}},
		{"Wild Card", "WILD_CARD", Label{}},
		{"", "THE_PREDATOR", Label{}},
		// From the ripping PC.
		{"COLUMBO_SEASON6AND7_DISC1", "COLUMBO_SEASON6AND7_DISC1", Label{Show: "Columbo", Season: 6, LastSeason: 7, Disc: 1}},
		{"Killjoys: Season One (Disc 1)", "KILLJOYS_S1D1_UPB1", Label{Show: "Killjoys", Season: 1, Disc: 1}},
		{"Killjoys: Season One (Disc 1)", "", Label{Show: "Killjoys", Season: 1, Disc: 1}},
		{"", "FRIENDS_THE_COMPLETE_SECOND_SEASON_DISC_3", Label{Show: "Friends", Season: 2, Disc: 3}},
		{"", "ONE_TREE_HILL_S3_D2", Label{Show: "One Tree Hill", Season: 3, Disc: 2}},
		{"", "LOST_SEASONS_1-2_DISC_4", Label{Show: "Lost", Season: 1, LastSeason: 2, Disc: 4}},
		{"", "BLAKES_7_SERIES_2_DISC_1", Label{Show: "Blakes 7", Season: 2, Disc: 1}},
	} {
		got := ParseLabel(c.name, c.label)
		got.From = ""
		if got != c.want {
			t.Errorf("ParseLabel(%q, %q) = %+v, want %+v", c.name, c.label, got, c.want)
		}
	}
}

// title makes a title with the given ID, length in minutes and size.
func title(id int, minutes float64, gb float64) makemkv.Title {
	secs := int(minutes * 60)
	return makemkv.Title{ID: id, Attrs: makemkv.Attrs{
		makemkv.AttrDuration:      fmt.Sprintf("%d:%02d:%02d", secs/3600, secs/60%60, secs%60),
		makemkv.AttrDiskSizeBytes: fmt.Sprint(int64(gb * 1e9)),
	}}
}

func ids(ts []makemkv.Title) string {
	var s []string
	for _, t := range ts {
		s = append(s, fmt.Sprint(t.ID))
	}
	return strings.Join(s, ",")
}

func TestEpisodesTypicalTVDisc(t *testing.T) {
	sel := Episodes([]makemkv.Title{
		title(0, 44*4+0.5, 8), // play all
		title(1, 44, 2), title(2, 43, 2.1), title(3, 45, 2.2), title(4, 44.5, 2),
		title(5, 3, 0.1),  // trailer
		title(6, 12, 0.5), // featurette
	})
	if ids(sel.Episodes) != "1,2,3,4" {
		t.Errorf("episodes = %s; left %+v", ids(sel.Episodes), sel.Left)
	}
	if len(sel.Warnings) != 0 {
		t.Errorf("warnings: %v", sel.Warnings)
	}
	reasons := map[int]string{}
	for _, l := range sel.Left {
		reasons[l.Title.ID] = l.Reason
	}
	if !strings.Contains(reasons[0], "play all") || !strings.Contains(reasons[5], "short") || !strings.Contains(reasons[6], "extra") {
		t.Errorf("reasons: %v", reasons)
	}
}

func TestEpisodesDoubleLengthAndDuplicates(t *testing.T) {
	a := title(2, 22, 1.05)
	dup := a
	dup.ID = 3
	sel := Episodes([]makemkv.Title{title(0, 45, 2), title(1, 22, 1), a, dup, title(4, 23, 1.1)})
	if ids(sel.Episodes) != "0,1,2,4" {
		t.Errorf("episodes = %s; left %+v", ids(sel.Episodes), sel.Left)
	}
	if len(sel.Warnings) != 1 || !strings.Contains(sel.Warnings[0], "twice as long") {
		t.Errorf("warnings: %v", sel.Warnings)
	}
}

func TestEpisodesNothingFound(t *testing.T) {
	sel := Episodes([]makemkv.Title{title(0, 5, 0.2), title(1, 8, 0.3)})
	if len(sel.Episodes) != 0 || len(sel.Warnings) == 0 || len(sel.Left) != 2 {
		t.Errorf("%+v", sel)
	}
}

func TestNames(t *testing.T) {
	if got := FileName("Friends", 1, 7); got != "Friends - S01E07" {
		t.Error(got)
	}
	if got := Range(1, 7, 6); got != "S01E07–S01E12" {
		t.Error(got)
	}
	if got := SeasonFolder(3); got != "Season 03" {
		t.Error(got)
	}
}

func TestEpisodesTwoEpisodePlayAllIsFlagged(t *testing.T) {
	sel := Episodes([]makemkv.Title{title(0, 88.2, 4), title(1, 44, 2), title(2, 44, 2.1)})
	if ids(sel.Episodes) != "1,2" || len(sel.Warnings) != 1 || !strings.Contains(sel.Warnings[0], "double episode") {
		t.Errorf("episodes %s, warnings %v", ids(sel.Episodes), sel.Warnings)
	}
}

// The two discs probed on the ripping PC.
func TestEpisodesRealDiscs(t *testing.T) {
	// Killjoys season 1 disc 1 (Blu-ray): a 5½-minute extra and five
	// episodes, two of them the same length to the second.
	kj := []makemkv.Title{title(0, 5.5, 0.007)}
	for i, secs := range []int{2553, 2553, 2555, 2557, 2557} {
		tt := title(i+1, float64(secs)/60, 8.8+float64(i)/1000)
		tt.Attrs[makemkv.AttrSegmentsMap] = fmt.Sprint(800 + i)
		kj = append(kj, tt)
	}
	sel := Episodes(kj)
	if ids(sel.Episodes) != "1,2,3,4,5" || len(sel.Warnings) != 0 || len(sel.Left) != 1 {
		t.Errorf("Killjoys: episodes %s, left %+v, warnings %v", ids(sel.Episodes), sel.Left, sel.Warnings)
	}
	// Without segment maps, different sizes still tell them apart.
	for i := range kj {
		delete(kj[i].Attrs, makemkv.AttrSegmentsMap)
	}
	if sel := Episodes(kj); ids(sel.Episodes) != "1,2,3,4,5" {
		t.Errorf("Killjoys without segment maps: %s", ids(sel.Episodes))
	}

	// Columbo seasons 6 and 7 disc 1 (DVD): three feature-length episodes.
	sel = Episodes([]makemkv.Title{title(0, 70.4, 2.3), title(1, 72.5, 2.31), title(2, 70.1, 2.29)})
	if ids(sel.Episodes) != "0,1,2" || len(sel.Warnings) != 0 {
		t.Errorf("Columbo: episodes %s, warnings %v", ids(sel.Episodes), sel.Warnings)
	}
}

func TestLooksLikeTV(t *testing.T) {
	killjoys := []makemkv.Title{title(0, 5.5, 0.007)}
	for i := 1; i <= 5; i++ {
		killjoys = append(killjoys, title(i, 42.5+float64(i)/60, 8.8+float64(i)/1000))
	}
	for name, c := range map[string]struct {
		titles []makemkv.Title
		want   bool
	}{
		"Killjoys disc":          {killjoys, true},
		"Columbo disc":           {[]makemkv.Title{title(0, 70.4, 2.3), title(1, 72.5, 2.31), title(2, 70.1, 2.29)}, true},
		"film":                   {[]makemkv.Title{title(0, 102.6, 28.9), title(1, 2, 0.1)}, false},
		"film and featurettes":   {[]makemkv.Title{title(0, 118, 30), title(1, 14, 1), title(2, 15, 1.1), title(3, 13.5, 0.9)}, false},
		"two cuts of a film":     {[]makemkv.Title{title(0, 121, 30), title(1, 128, 31)}, false},
		"film and a documentary": {[]makemkv.Title{title(0, 95, 25), title(1, 60, 4)}, false},
		// Not counted as TV without -B: a film under 100 minutes with a couple of
		// short extras looks the same (the film is "longer than the episodes").
		"Columbo, mixed lengths":           {[]makemkv.Title{title(0, 73.4, 2.4), title(1, 95.2, 3.1), title(2, 74.8, 2.4)}, false},
		"film and featurettes, short film": {[]makemkv.Title{title(0, 95, 25), title(1, 25, 1), title(2, 24, 1)}, false},
	} {
		if got := LooksLikeTV(Episodes(c.titles)); got != c.want {
			t.Errorf("%s: LooksLikeTV = %v, want %v", name, got, c.want)
		}
	}
}

// DVD cells are numbered from 1 in each title set, so two different
// episodes can have the same segment map; they aren't duplicates unless
// they are also the same length.
func TestEpisodesDVDSameSegmentMapDifferentEpisodes(t *testing.T) {
	withMap := func(tt makemkv.Title, m string) makemkv.Title {
		tt.Attrs = maps.Clone(tt.Attrs)
		tt.Attrs[makemkv.AttrSegmentsMap] = m
		return tt
	}
	a := withMap(title(1, 73.4, 4.1), "1-12")
	b := withMap(title(2, 75.1, 4.1), "1-12") // another episode, same cells and about the same size
	dup := withMap(title(3, 73.4, 4.1), "1-12")
	sel := Episodes([]makemkv.Title{a, b, dup})
	if ids(sel.Episodes) != "1,2" {
		t.Errorf("episodes = %s; left %+v", ids(sel.Episodes), sel.Left)
	}
	if len(sel.Left) != 1 || sel.Left[0].Title.ID != 3 || sel.Left[0].Reason != "same as title 1" {
		t.Errorf("left = %+v", sel.Left)
	}
}

// Columbo mixes 70- and 90-minute episodes on one disc: all are kept, the
// odd ones flagged; extras well under the episode length are still left out.
func TestEpisodesMixedLengths(t *testing.T) {
	sel := Episodes([]makemkv.Title{title(0, 95.2, 3.1), title(1, 73.4, 2.4), title(2, 96.8, 3.2), title(3, 25, 0.8), title(4, 5, 0.1)})
	if ids(sel.Episodes) != "0,1,2" {
		t.Errorf("episodes = %s; left %+v", ids(sel.Episodes), sel.Left)
	}
	if len(sel.Left) != 2 || sel.Left[0].Title.ID != 3 || sel.Left[0].Reason != "much shorter than the episodes (extra)" {
		t.Errorf("left = %+v", sel.Left)
	}
	if len(sel.Warnings) != 1 || !strings.Contains(sel.Warnings[0], "title 1 (1h13m24s) is shorter than most episodes") {
		t.Errorf("warnings = %q", sel.Warnings)
	}
	// More 70-minute ones: the 90-minute one is kept and flagged as longer.
	sel = Episodes([]makemkv.Title{title(0, 73.4, 2.4), title(1, 95.2, 3.1), title(2, 74.8, 2.4)})
	if ids(sel.Episodes) != "0,1,2" || len(sel.Warnings) != 1 || !strings.Contains(sel.Warnings[0], "title 1 (1h35m12s) is longer than most episodes") {
		t.Errorf("episodes = %s, warnings = %q", ids(sel.Episodes), sel.Warnings)
	}
}
