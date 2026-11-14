package boxset

import (
	"fmt"
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
