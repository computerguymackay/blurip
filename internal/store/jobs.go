package store

import (
	"context"
	"database/sql"
	"errors"
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
	Name        string
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

	// Filled in by History.
	DiscName    string
	DriveDevice string
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
		"SELECT id, fingerprint, name, volume_label, disc_type, first_seen FROM discs WHERE fingerprint = ?",
		d.Fingerprint).Scan(&out.ID, &out.Fingerprint, &out.Name, &out.VolumeLabel, &out.Type, &first)
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

// StartJob records a new running job and returns its ID.
func (s *Store) StartJob(ctx context.Context, discID, driveID int64, start time.Time) (int64, error) {
	r, err := s.db.ExecContext(ctx,
		"INSERT INTO jobs(disc_id, drive_id, started_at, status) VALUES(?, ?, ?, ?)",
		nullID(discID), nullID(driveID), fmtTime(start), StatusRunning)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

// FinishJob records the outcome of a running job.
func (s *Store) FinishJob(ctx context.Context, j Job) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET finished_at = ?, status = ?, failure_reason = ?, output_path = ?, bytes_written = ?
		WHERE id = ?`,
		fmtTime(j.FinishedAt), j.Status, j.FailureReason, j.OutputPath, j.BytesWritten, j.ID)
	return err
}

// RecordJob inserts a job that is already finished (e.g. skipped, or failed
// before the disc could be identified) and returns its ID.
func (s *Store) RecordJob(ctx context.Context, j Job) (int64, error) {
	r, err := s.db.ExecContext(ctx, `
		INSERT INTO jobs(disc_id, drive_id, started_at, finished_at, status, failure_reason, output_path, bytes_written)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		nullID(j.DiscID), nullID(j.DriveID), fmtTime(j.StartedAt), fmtTime(j.FinishedAt),
		j.Status, j.FailureReason, j.OutputPath, j.BytesWritten)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
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
		       COALESCE(NULLIF(d.name, ''), d.volume_label, ''), COALESCE(dr.device, '')
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
		if err := rows.Scan(&j.ID, &j.DiscID, &j.DriveID, &started, &finished, &j.Status, &j.FailureReason,
			&j.OutputPath, &j.BytesWritten, &j.DiscName, &j.DriveDevice); err != nil {
			return nil, err
		}
		j.StartedAt, j.FinishedAt = parseTime(started), parseTime(finished)
		out = append(out, j)
	}
	return out, rows.Err()
}
