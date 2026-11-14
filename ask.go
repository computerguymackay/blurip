package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/computerguymackay/blurip/internal/makemkv"
	"github.com/computerguymackay/blurip/internal/prompt"
	"github.com/computerguymackay/blurip/internal/rip"
)

// ripChoice is what to do with a disc.
type ripChoice struct {
	Skip   bool
	Titles []makemkv.Title
	Name   string // output folder name

	// Box sets: Folder (e.g. "Friends/Season 01") under the output folder
	// that the episodes go into, one file name per title, and a summary
	// such as "Friends S01E07–S01E12".
	Folder    string
	FileNames []string
	Episodes  string
}

// askWhatToRip shows the disc's titles and asks which to rip and what to
// call the folder ("ask every time" mode).
func (s *session) askWhatToRip(info *makemkv.Info, main makemkv.Title, name string) (ripChoice, error) {
	printDisc(s.out, info)
	fmt.Fprintln(s.out)

	what, err := s.ask.Choice("What do you want to rip?", []prompt.Option{
		{Value: "main", Label: fmt.Sprintf("The main feature (title %d, %s)", main.ID, main.Duration())},
		{Value: "choose", Label: "Choose titles"},
		{Value: "skip", Label: "Skip this disc"},
	}, "main")
	if err != nil {
		return ripChoice{}, err
	}

	var titles []makemkv.Title
	switch what {
	case "skip":
		return ripChoice{Skip: true}, nil
	case "main":
		titles = []makemkv.Title{main}
	case "choose":
		byID := map[int]makemkv.Title{}
		for _, t := range info.Titles {
			byID[t.ID] = t
		}
		ids, err := s.ask.Numbers("Titles to rip (e.g. 1,3 or 0-2)", []int{main.ID},
			func(id int) bool { _, ok := byID[id]; return ok })
		if err != nil {
			return ripChoice{}, err
		}
		for _, id := range ids {
			titles = append(titles, byID[id])
		}
	}

	answer, err := s.ask.String("Folder name", name)
	if err != nil {
		return ripChoice{}, err
	}
	return ripChoice{Titles: titles, Name: rip.FolderName(answer, name)}, nil
}

func titleIDs(ts []makemkv.Title) string {
	ids := make([]string, len(ts))
	for i, t := range ts {
		ids[i] = strconv.Itoa(t.ID)
	}
	return strings.Join(ids, ", ")
}
