package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Job statuses.
const (
	StatusRunning     = "running"
	StatusSucceeded   = "succeeded"
	StatusFailed      = "failed"
	StatusInterrupted = "interrupted"
	StatusSkipped     = "skipped"
)

// Disc is a disc blurip has seen.
type Disc struct {
	ID          int64
	Fingerprint string
	Name        string // from the disc (MakeMKV's name or the volume label)
	GivenName   string // the name the user gave it, if any (see SetDiscName)
	VolumeLabel string
	Type        string
	FirstSeen   time.Time
}

// Job is one attempt to rip (or a decision to skip) a disc.
type Job struct {
	ID            int64
	DiscID        int64 // 0 if the disc could not be identified
	DriveID       int64 // 0 if unknown
	StartedAt     time.Time
	FinishedAt    time.Time // zero while running, or if never finished
	Status        string
	FailureReason string
	OutputPath    string
	BytesWritten  int64
	Note          string // what else the job changed (fixing a box set disc)

	// How the disc was handled.
	DriveMode  string // single | sequential | parallel ("" for jobs from older versions)
	DrivesUsed string // drives blurip was using, e.g. "DVD drive (sr0), Blu-ray drive (sr1)"
	RipsAtOnce int    // most rips running at the same time during this one (1 = alone; 0 = never ripped)
	ReadTime   time.Duration
	WaitTime   time.Duration // queued behind another drive's rip
	RipTime    time.Duration

	// Filled in by History.
	DiscName    string
	DiscType    string // MakeMKV's disc type, e.g. "Blu-ray disc"
	DriveDevice string
	DriveLabel  string // "Blu-ray drive (sr1)", or "" if the drive is unknown
}

// Duration returns how long the job ran, or 0 if it never finished.
func (j Job) Duration() time.Duration {
	if j.FinishedAt.IsZero() {
		return 0
	}
	return j.FinishedAt.Sub(j.StartedAt)
}

const timeFormat = time.RFC3339Nano

func fmtTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(timeFormat)
}

func parseTime(s sql.NullString) time.Time {
	if !s.Valid {
		return time.Time{}
	}
	t, _ := time.Parse(timeFormat, s.String)
	return t.Local()
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// UpsertDisc returns the disc with this fingerprint, creating it if new.
// Name, label and type are refreshed if they have changed.
func (s *Store) UpsertDisc(ctx context.Context, d Disc, now time.Time) (Disc, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO discs(fingerprint, name, volume_label, disc_type, first_seen) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(fingerprint) DO UPDATE SET
			name = excluded.name, volume_label = excluded.volume_label, disc_type = excluded.disc_type`,
		d.Fingerprint, d.Name, d.VolumeLabel, d.Type, fmtTime(now))
	if err != nil {
		return Disc{}, err
	}
	var out Disc
	var first sql.NullString
	err = s.db.QueryRowContext(ctx,
		"SELECT id, fingerprint, name, given_name, volume_label, disc_type, first_seen FROM discs WHERE fingerprint = ?",
		d.Fingerprint).Scan(&out.ID, &out.Fingerprint, &out.Name, &out.GivenName, &out.VolumeLabel, &out.Type, &first)
	out.FirstSeen = parseTime(first)
	return out, err
}

// DriveID returns the ID of the drive with this device path, or 0.
func (s *Store) DriveID(ctx context.Context, device string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, "SELECT id FROM drives WHERE device = ?", device).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// StartJob records a new running job and returns its ID. Status, finish
// time and the outcome fields are ignored.
func (s *Store) StartJob(ctx context.Context, j Job) (int64, error) {
	j.Status, j.FinishedAt = StatusRunning, time.Time{}
	return s.insertJob(ctx, j)
}

// FinishJob records the outcome of a running job.
func (s *Store) FinishJob(ctx context.Context, j Job) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET finished_at = ?, status = ?, failure_reason = ?, output_path = ?, bytes_written = ?,
		       rips_at_once = ?, rip_seconds = ?, note = ?
		WHERE id = ?`,
		fmtTime(j.FinishedAt), j.Status, j.FailureReason, j.OutputPath, j.BytesWritten,
		j.RipsAtOnce, j.RipTime.Seconds(), j.Note, j.ID)
	return err
}

// SetDriveLeftDisc records the disc left in a drive after blurip handled
// it (it couldn't be ejected, or ejecting is off); discID 0 clears it.
func (s *Store) SetDriveLeftDisc(ctx context.Context, device string, discID int64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE drives SET left_disc_id = ? WHERE device = ?", nullID(discID), device)
	return err
}

// DriveLeftDisc returns the disc left in a drive (see SetDriveLeftDisc), or 0.
func (s *Store) DriveLeftDisc(ctx context.Context, device string) (int64, error) {
	var id sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT left_disc_id FROM drives WHERE device = ?", device).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id.Int64, err
}

// SetDiscName records the name the user gave a disc. It is shown in the
// history and used the next time the disc is inserted.
func (s *Store) SetDiscName(ctx context.Context, discID int64, name string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE discs SET given_name = ? WHERE id = ?", name, discID)
	return err
}

// SetJobOutputPath changes where a finished job's rip is (after a rename).
func (s *Store) SetJobOutputPath(ctx context.Context, jobID int64, path string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE jobs SET output_path = ? WHERE id = ?", path, jobID)
	return err
}

// MoveDiscRips points every job of a disc that ripped to oldPath at
// newPath (its rip folder was renamed).
func (s *Store) MoveDiscRips(ctx context.Context, discID int64, oldPath, newPath string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE jobs SET output_path = ? WHERE disc_id = ? AND output_path = ?", newPath, discID, oldPath)
	return err
}

// FilmRips returns the last successful rip of every disc that isn't part
// of a box set, newest first.
func (s *Store) FilmRips(ctx context.Context) ([]Job, error) {
	return s.queryJobs(ctx, `
		WHERE j.status = ? AND j.output_path != '' AND j.disc_id IS NOT NULL
		AND j.disc_id NOT IN (SELECT disc_id FROM disc_episodes)
		AND j.id = (SELECT j2.id FROM jobs j2 WHERE j2.disc_id = j.disc_id AND j2.status = ?
		            ORDER BY j2.started_at DESC, j2.id DESC LIMIT 1)
		ORDER BY j.started_at DESC, j.id DESC`, StatusSucceeded, StatusSucceeded)
}

// RecordJob inserts a job that is already finished (e.g. skipped, or failed
// before the disc could be identified) and returns its ID.
func (s *Store) RecordJob(ctx context.Context, j Job) (int64, error) { return s.insertJob(ctx, j) }

func (s *Store) insertJob(ctx context.Context, j Job) (int64, error) {
	r, err := s.db.ExecContext(ctx, `
		INSERT INTO jobs(disc_id, drive_id, started_at, finished_at, status, failure_reason, output_path, bytes_written,
		                 drive_mode, drives_used, rips_at_once, read_seconds, wait_seconds, rip_seconds, note)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullID(j.DiscID), nullID(j.DriveID), fmtTime(j.StartedAt), fmtTime(j.FinishedAt),
		j.Status, j.FailureReason, j.OutputPath, j.BytesWritten,
		j.DriveMode, j.DrivesUsed, j.RipsAtOnce, j.ReadTime.Seconds(), j.WaitTime.Seconds(), j.RipTime.Seconds(), j.Note)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

// Job returns one job by ID.
func (s *Store) Job(ctx context.Context, id int64) (Job, error) {
	jobs, err := s.queryJobs(ctx, "WHERE j.id = ?", id)
	if err != nil {
		return Job{}, err
	}
	if len(jobs) == 0 {
		return Job{}, fmt.Errorf("no job %d", id)
	}
	return jobs[0], nil
}

// AddJobTitle records which title (and track selection) a job ripped.
func (s *Store) AddJobTitle(ctx context.Context, jobID int64, titleID int, audio, subtitles string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO job_titles(job_id, title_id, audio_selection, subtitle_selection) VALUES(?, ?, ?, ?)",
		jobID, titleID, audio, subtitles)
	return err
}

// LastSuccess returns the most recent successful job for a disc.
func (s *Store) LastSuccess(ctx context.Context, discID int64) (Job, bool, error) {
	jobs, err := s.queryJobs(ctx, "WHERE j.disc_id = ? AND j.status = ? ORDER BY j.started_at DESC, j.id DESC LIMIT 1",
		discID, StatusSucceeded)
	if err != nil || len(jobs) == 0 {
		return Job{}, false, err
	}
	return jobs[0], true, nil
}

// RecoverInterrupted marks every job still "running" as interrupted. Call it
// at startup while holding the Lock: any such job belongs to a blurip that
// was killed or crashed. It returns the affected jobs.
func (s *Store) RecoverInterrupted(ctx context.Context) ([]Job, error) {
	jobs, err := s.queryJobs(ctx, "WHERE j.status = ? ORDER BY j.id", StatusRunning)
	if err != nil || len(jobs) == 0 {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx,
		"UPDATE jobs SET status = ?, failure_reason = ? WHERE status = ?",
		StatusInterrupted, "blurip stopped unexpectedly before the rip finished", StatusRunning)
	return jobs, err
}

// HistoryFilter selects jobs for History.
type HistoryFilter struct {
	FailedOnly bool // failed and interrupted jobs only
	Limit      int  // 0 = no limit
}

// History returns jobs newest first.
func (s *Store) History(ctx context.Context, f HistoryFilter) ([]Job, error) {
	q := ""
	var args []any
	if f.FailedOnly {
		q = "WHERE j.status IN (?, ?) "
		args = append(args, StatusFailed, StatusInterrupted)
	}
	q += "ORDER BY j.started_at DESC, j.id DESC"
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}
	return s.queryJobs(ctx, q, args...)
}

func (s *Store) queryJobs(ctx context.Context, where string, args ...any) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT j.id, COALESCE(j.disc_id, 0), COALESCE(j.drive_id, 0), j.started_at, j.finished_at,
		       j.status, j.failure_reason, j.output_path, j.bytes_written,
		       COALESCE(NULLIF(d.given_name, ''), NULLIF(d.name, ''), d.volume_label, ''), COALESCE(d.disc_type, ''), COALESCE(dr.device, ''), COALESCE(dr.nickname, ''),
		       j.drive_mode, j.drives_used, j.rips_at_once, j.read_seconds, j.wait_seconds, j.rip_seconds, j.note
		FROM jobs j
		LEFT JOIN discs d ON d.id = j.disc_id
		LEFT JOIN drives dr ON dr.id = j.drive_id
		`+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		var started, finished sql.NullString
		var nickname string
		var readS, waitS, ripS float64
		if err := rows.Scan(&j.ID, &j.DiscID, &j.DriveID, &started, &finished, &j.Status, &j.FailureReason,
			&j.OutputPath, &j.BytesWritten, &j.DiscName, &j.DiscType, &j.DriveDevice, &nickname,
			&j.DriveMode, &j.DrivesUsed, &j.RipsAtOnce, &readS, &waitS, &ripS, &j.Note); err != nil {
			return nil, err
		}
		if j.DriveDevice != "" {
			j.DriveLabel = Drive{Device: j.DriveDevice, Nickname: nickname}.Label()
		}
		j.StartedAt, j.FinishedAt = parseTime(started), parseTime(finished)
		j.ReadTime, j.WaitTime, j.RipTime = seconds(readS), seconds(waitS), seconds(ripS)
		out = append(out, j)
	}
	return out, rows.Err()
}

func seconds(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

// Stats counts jobs by outcome.
type Stats struct {
	Succeeded, Failed, Interrupted, Skipped int
	BytesRipped                             int64 // total size of successful rips
}

// JobStats returns counts of all jobs by outcome.
func (s *Store) JobStats(ctx context.Context) (Stats, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT status, COUNT(*), COALESCE(SUM(bytes_written), 0) FROM jobs GROUP BY status")
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()
	var st Stats
	for rows.Next() {
		var status string
		var n int
		var bytes int64
		if err := rows.Scan(&status, &n, &bytes); err != nil {
			return Stats{}, err
		}
		switch status {
		case StatusSucceeded:
			st.Succeeded, st.BytesRipped = n, bytes
		case StatusFailed:
			st.Failed = n
		case StatusInterrupted:
			st.Interrupted = n
		case StatusSkipped:
			st.Skipped = n
		}
	}
	return st, rows.Err()
}
