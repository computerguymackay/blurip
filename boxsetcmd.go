package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/computerguymackay/blurip/internal/boxset"
	"github.com/computerguymackay/blurip/internal/store"
)

const boxSetUsage = `Usage: blurip boxset                               list box sets and how far each has got
       blurip boxset set SHOW SEASON next EPISODE   the next disc starts at this episode
       blurip boxset set SHOW SEASON disc NUMBER    the last disc ripped was this one
       blurip boxset fix SHOW SEASON [DISC]         put a disc's episodes in the right order, or delete
                                                    one (an extra), without the disc; later episodes
                                                    are renumbered to match; its first episode can
                                                    be changed, and a gap in the numbering next to
                                                    the disc closed. Answer b to go back. Without DISC
                                                    it can also rename the show or change the season
                                                    (every disc, its files and the encode queue)
       blurip boxset forget SHOW SEASON             forget a box set (files and history stay)

SHOW is the show's name as listed (any case); quote it if it has spaces.
`

func runBoxSet(ctx context.Context, dbPath string, args []string) error {
	if len(args) > 0 && args[0] == "fix" {
		return runBoxSetFix(ctx, dbPath, args[1:])
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close()
	if len(args) == 0 {
		list, err := s.BoxSets(ctx)
		if err != nil {
			return err
		}
		return printBoxSets(os.Stdout, list)
	}
	switch args[0] {
	case "-h", "help", "--help":
		fmt.Print(boxSetUsage)
		return nil
	case "set":
		if len(args) != 5 {
			break
		}
		season, err1 := strconv.Atoi(args[2])
		n, err2 := strconv.Atoi(args[4])
		if err1 != nil || err2 != nil {
			return fmt.Errorf("SEASON and %s must be numbers", strings.ToUpper(args[3]))
		}
		next, disc := -1, -1
		switch args[3] {
		case "next":
			if n < 1 {
				return errors.New("the episode number must be 1 or more")
			}
			next = n
		case "disc":
			if n < 0 {
				return errors.New("the disc number can't be negative")
			}
			disc = n
		default:
			return fmt.Errorf("expected next or disc, got %q", args[3])
		}
		if err := s.SetBoxSetProgress(ctx, args[1], season, next, disc, time.Now()); err != nil {
			return boxSetErr(err)
		}
		b, _, err := s.FindBoxSet(ctx, args[1], season)
		if err != nil {
			return err
		}
		fmt.Printf("%s season %d: the next disc starts at %s (last disc: %s).\n",
			b.Show, b.Season, boxset.Code(b.Season, b.NextEpisode), discWord(b.LastDisc))
		return nil
	case "forget":
		if len(args) != 3 {
			break
		}
		season, err := strconv.Atoi(args[2])
		if err != nil {
			return errors.New("SEASON must be a number")
		}
		if err := s.ForgetBoxSet(ctx, args[1], season); err != nil {
			return boxSetErr(err)
		}
		fmt.Printf("Forgot %s season %d. Its next disc will be asked about as a new season.\n", args[1], season)
		return nil
	}
	fmt.Fprint(os.Stderr, boxSetUsage)
	return fmt.Errorf("unknown boxset command %q", strings.Join(args, " "))
}

func boxSetErr(err error) error {
	if errors.Is(err, store.ErrUnknownBoxSet) {
		return fmt.Errorf("%w; run `blurip boxset` to list them", err)
	}
	return err
}

func printBoxSets(w io.Writer, list []store.BoxSet) error {
	if len(list) == 0 {
		fmt.Fprintln(w, "No box sets yet. Rip one with `blurip -B`.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SHOW\tSEASON\tDISCS\tEPISODES\tLAST DISC\tNEXT\tUPDATED")
	for _, b := range list {
		last := "-"
		if b.LastDisc > 0 {
			last = strconv.Itoa(b.LastDisc)
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%s\t%s\t%s\n", b.Show, b.Season, b.Discs, b.Episodes, last,
			boxset.Code(b.Season, b.NextEpisode), b.UpdatedAt.Local().Format("2006-01-02 15:04"))
	}
	return tw.Flush()
}

// episodeSummary is "Friends S01E04–S01E06" for a disc numbered in
// box-set mode, or "".
func episodeSummary(ctx context.Context, s *store.Store, discID int64) string {
	if discID == 0 {
		return ""
	}
	d, ok, err := s.EpisodesForDisc(ctx, discID)
	if err != nil || !ok {
		return ""
	}
	return d.BoxSet.Show + " " + boxset.Range(d.BoxSet.Season, d.Episodes[0].Episode, d.Count())
}
