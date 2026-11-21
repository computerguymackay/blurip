package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// BoxSet is one show's season being ripped in box-set mode.
type BoxSet struct {
	ID          int64
	Show        string
	Season      int
	LastDisc    int // 0 = unknown
	NextEpisode int
	StartedAt   time.Time
	UpdatedAt   time.Time

	// Filled in by BoxSets.
	Discs    int // discs numbered
	Episodes int // episodes numbered
}

// Episode is one title of a disc and the episode it became. A title
// split by chapters is several episodes: Episode is the first, and
// Chapters each one's chapter range ("1-5,6-10"; "" = one episode).
type Episode struct {
	TitleID  int
	Episode  int
	Chapters string
}

// Parts is how many episodes the title is.
func (e Episode) Parts() int { return parts(e.Chapters) }

func parts(chapters string) int {
	if chapters == "" {
		return 1
	}
	return strings.Count(chapters, ",") + 1
}

// partsSQL counts a disc_episodes row's episodes, like parts.
const partsSQL = "(CASE WHEN e.chapters = '' THEN 1 ELSE LENGTH(e.chapters) - LENGTH(REPLACE(e.chapters, ',', '')) + 1 END)"

// DiscEpisodes is how one disc's titles were numbered.
type DiscEpisodes struct {
	BoxSet   BoxSet
	DiscNo   int
	Episodes []Episode // in rip order
}

// Count is how many episodes the disc has.
func (d DiscEpisodes) Count() int {
	n := 0
	for _, e := range d.Episodes {
		n += e.Parts()
	}
	return n
}

// Split reports whether any of the disc's titles is split by chapters.
func (d DiscEpisodes) Split() bool {
	for _, e := range d.Episodes {
		if e.Chapters != "" {
			return true
		}
	}
	return false
}

// ErrUnknownBoxSet is returned for a show and season that isn't recorded.
var ErrUnknownBoxSet = errors.New("no box set with that show and season")

const boxSetCols = "id, show, season, last_disc, next_episode, started_at, updated_at"

func scanBoxSet(row interface{ Scan(...any) error }) (BoxSet, error) {
	var b BoxSet
	var started, updated sql.NullString
	err := row.Scan(&b.ID, &b.Show, &b.Season, &b.LastDisc, &b.NextEpisode, &started, &updated)
	b.StartedAt, b.UpdatedAt = parseTime(started), parseTime(updated)
	return b, err
}

// FindBoxSet returns the box set for show (any case) and season.
func (s *Store) FindBoxSet(ctx context.Context, show string, season int) (BoxSet, bool, error) {
	b, err := scanBoxSet(s.db.QueryRowContext(ctx,
		"SELECT "+boxSetCols+" FROM box_sets WHERE show = ? AND season = ?", show, season))
	if errors.Is(err, sql.ErrNoRows) {
		return BoxSet{}, false, nil
	}
	return b, err == nil, err
}

// BoxSets returns every box set, most recently updated first, with how
// many discs and episodes each has.
func (s *Store) BoxSets(ctx context.Context) ([]BoxSet, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT b.id, b.show, b.season, b.last_disc, b.next_episode, b.started_at, b.updated_at,
			COUNT(DISTINCT e.disc_id), COALESCE(SUM(`+partsSQL+`), 0)
		FROM box_sets b LEFT JOIN disc_episodes e ON e.box_set_id = b.id
		GROUP BY b.id ORDER BY b.updated_at DESC, b.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BoxSet
	for rows.Next() {
		var b BoxSet
		var started, updated sql.NullString
		if err := rows.Scan(&b.ID, &b.Show, &b.Season, &b.LastDisc, &b.NextEpisode, &started, &updated, &b.Discs, &b.Episodes); err != nil {
			return nil, err
		}
		b.StartedAt, b.UpdatedAt = parseTime(started), parseTime(updated)
		out = append(out, b)
	}
	return out, rows.Err()
}

// EpisodesForDisc returns how a disc was numbered, if it was.
func (s *Store) EpisodesForDisc(ctx context.Context, discID int64) (DiscEpisodes, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT box_set_id, disc_no, title_id, episode, chapters FROM disc_episodes
		WHERE disc_id = ? ORDER BY position`, discID)
	if err != nil {
		return DiscEpisodes{}, false, err
	}
	defer rows.Close()
	var d DiscEpisodes
	var boxID int64
	for rows.Next() {
		var e Episode
		if err := rows.Scan(&boxID, &d.DiscNo, &e.TitleID, &e.Episode, &e.Chapters); err != nil {
			return DiscEpisodes{}, false, err
		}
		d.Episodes = append(d.Episodes, e)
	}
	if err := rows.Err(); err != nil || len(d.Episodes) == 0 {
		return DiscEpisodes{}, false, err
	}
	d.BoxSet, err = scanBoxSet(s.db.QueryRowContext(ctx, "SELECT "+boxSetCols+" FROM box_sets WHERE id = ?", boxID))
	if err != nil {
		return DiscEpisodes{}, false, err
	}
	return d, true, nil
}

// AssignEpisodes records that a disc's titles are these episodes of show
// and season (creating the box set if new), replacing any earlier
// numbering of the disc. If the disc reaches the newest episodes, the box
// set's next episode moves past them and its last disc becomes discNo;
// neither ever goes back.
func (s *Store) AssignEpisodes(ctx context.Context, show string, season, discNo int, discID int64, eps []Episode, now time.Time) (BoxSet, error) {
	var out BoxSet
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO box_sets(show, season, started_at, updated_at) VALUES(?, ?, ?, ?)
			ON CONFLICT(show, season) DO NOTHING`, show, season, fmtTime(now), fmtTime(now)); err != nil {
			return err
		}
		b, err := scanBoxSet(tx.QueryRowContext(ctx, "SELECT "+boxSetCols+" FROM box_sets WHERE show = ? AND season = ?", show, season))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM disc_episodes WHERE disc_id = ?", discID); err != nil {
			return err
		}
		after := 0 // the episode after this disc's last
		for i, e := range eps {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO disc_episodes(disc_id, box_set_id, disc_no, title_id, position, episode, chapters) VALUES(?, ?, ?, ?, ?, ?, ?)`,
				discID, b.ID, discNo, e.TitleID, i, e.Episode, e.Chapters); err != nil {
				return err
			}
			after = max(after, e.Episode+e.Parts())
		}
		// Only a disc that reaches the newest episodes becomes the last
		// disc; renumbering an earlier disc leaves both alone.
		if after >= b.NextEpisode {
			b.NextEpisode, b.LastDisc = after, discNo
		}
		if _, err := tx.ExecContext(ctx, "UPDATE box_sets SET last_disc = ?, next_episode = ?, updated_at = ? WHERE id = ?",
			b.LastDisc, b.NextEpisode, fmtTime(now), b.ID); err != nil {
			return err
		}
		b.UpdatedAt = now
		out = b
		return nil
	})
	return out, err
}

// NumberedEpisode is one title of a box set disc and the episode it is
// (its first, if split by chapters: see Episode).
type NumberedEpisode struct {
	DiscID   int64
	TitleID  int
	Episode  int
	Chapters string
}

// Parts is how many episodes the title is.
func (e NumberedEpisode) Parts() int { return parts(e.Chapters) }

// BoxSetEpisodes returns every numbered title of a box set, by episode.
func (s *Store) BoxSetEpisodes(ctx context.Context, boxSetID int64) ([]NumberedEpisode, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT disc_id, title_id, episode, chapters FROM disc_episodes WHERE box_set_id = ? ORDER BY episode, disc_id, position", boxSetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NumberedEpisode
	for rows.Next() {
		var e NumberedEpisode
		if err := rows.Scan(&e.DiscID, &e.TitleID, &e.Episode, &e.Chapters); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// BoxSetDisc is one numbered disc of a box set.
type BoxSetDisc struct {
	DiscID   int64
	DiscNo   int // 0 = unknown
	Name     string
	Type     string // MakeMKV's disc type, e.g. "DVD disc"
	First    int    // its first episode
	Episodes int
}

// BoxSetDiscs returns a box set's numbered discs, in episode order.
func (s *Store) BoxSetDiscs(ctx context.Context, boxSetID int64) ([]BoxSetDisc, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.disc_id, MAX(e.disc_no), COALESCE(NULLIF(d.given_name, ''), NULLIF(d.name, ''), d.volume_label, ''),
		       COALESCE(d.disc_type, ''), MIN(e.episode), SUM(`+partsSQL+`)
		FROM disc_episodes e LEFT JOIN discs d ON d.id = e.disc_id
		WHERE e.box_set_id = ? GROUP BY e.disc_id ORDER BY MIN(e.episode)`, boxSetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BoxSetDisc
	for rows.Next() {
		var d BoxSetDisc
		if err := rows.Scan(&d.DiscID, &d.DiscNo, &d.Name, &d.Type, &d.First, &d.Episodes); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RenumberDisc replaces a box set disc's numbering with eps (fixing the
// disc) and moves every other disc's episodes after episode after on by
// delta, along with the box set's next episode: one more or fewer
// episodes on this disc shifts the rest of the season.
func (s *Store) RenumberDisc(ctx context.Context, boxSetID, discID int64, discNo int, eps []Episode, after, delta int, now time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if delta != 0 {
			if _, err := tx.ExecContext(ctx,
				"UPDATE disc_episodes SET episode = episode + ? WHERE box_set_id = ? AND disc_id != ? AND episode > ?",
				delta, boxSetID, discID, after); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM disc_episodes WHERE disc_id = ?", discID); err != nil {
			return err
		}
		for i, e := range eps {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO disc_episodes(disc_id, box_set_id, disc_no, title_id, position, episode, chapters) VALUES(?, ?, ?, ?, ?, ?, ?)`,
				discID, boxSetID, discNo, e.TitleID, i, e.Episode, e.Chapters); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "UPDATE box_sets SET next_episode = MAX(1, next_episode + ?), updated_at = ? WHERE id = ?",
			delta, fmtTime(now), boxSetID)
		return err
	})
}

// SetBoxSetProgress corrects a box set's next episode and/or last disc
// (a negative value leaves it alone).
func (s *Store) SetBoxSetProgress(ctx context.Context, show string, season, nextEpisode, lastDisc int, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE box_sets SET
			next_episode = CASE WHEN ? >= 1 THEN ? ELSE next_episode END,
			last_disc    = CASE WHEN ? >= 0 THEN ? ELSE last_disc END,
			updated_at   = ?
		WHERE show = ? AND season = ?`,
		nextEpisode, nextEpisode, lastDisc, lastDisc, fmtTime(now), show, season)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s season %d", ErrUnknownBoxSet, show, season)
	}
	return nil
}

// ForgetBoxSet removes a box set and its episode numbering. Jobs, discs and
// ripped files are left alone.
func (s *Store) ForgetBoxSet(ctx context.Context, show string, season int) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRowContext(ctx, "SELECT id FROM box_sets WHERE show = ? AND season = ?", show, season).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s season %d", ErrUnknownBoxSet, show, season)
		} else if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM disc_episodes WHERE box_set_id = ?", id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM box_sets WHERE id = ?", id)
		return err
	})
}

// ErrBoxSetExists is returned when renaming a box set to a show and
// season that already has one.
var ErrBoxSetExists = errors.New("there is already a box set with that show and season")

// RenameBoxSet gives a box set another show name and/or season. dirs are
// rip folders that moved (old, new): its discs' jobs are pointed at the
// new one. The show's foreign-language answer is carried over to the new
// name, unless that show has one already.
func (s *Store) RenameBoxSet(ctx context.Context, id int64, show string, season int, dirs [][2]string, now time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var oldShow string
		if err := tx.QueryRowContext(ctx, "SELECT show FROM box_sets WHERE id = ?", id).Scan(&oldShow); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrUnknownBoxSet
			}
			return err
		}
		var other int64
		err := tx.QueryRowContext(ctx, "SELECT id FROM box_sets WHERE show = ? AND season = ? AND id != ?", show, season, id).Scan(&other)
		switch {
		case err == nil:
			return fmt.Errorf("%w: %s season %d", ErrBoxSetExists, show, season)
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE box_sets SET show = ?, season = ?, updated_at = ? WHERE id = ?",
			show, season, fmtTime(now), id); err != nil {
			return err
		}
		for _, d := range dirs {
			if _, err := tx.ExecContext(ctx, `
				UPDATE jobs SET output_path = ? WHERE output_path = ?
				AND disc_id IN (SELECT disc_id FROM disc_episodes WHERE box_set_id = ?)`, d[1], d[0], id); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO show_tracks(show, all_tracks) SELECT ?, all_tracks FROM show_tracks WHERE show = ?", show, oldShow)
		return err
	})
}
