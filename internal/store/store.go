// Package store owns blurip's SQLite database: opening it, applying schema
// migrations and reading/writing settings and drives.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// EnvDB is the environment variable that overrides the database path.
const EnvDB = "BLURIP_DB"

// DefaultPath returns the database path: $BLURIP_DB if set, otherwise
// <user config dir>/blurip/blurip.db (normally ~/.config/blurip/blurip.db).
func DefaultPath() (string, error) {
	if p := os.Getenv(EnvDB); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("finding config directory: %w", err)
	}
	return filepath.Join(dir, "blurip", "blurip.db"), nil
}

// Store is an open blurip database.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (creating if necessary) the database at path and applies any
// pending migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating database directory: %w", err)
	}
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "foreign_keys(1)", "busy_timeout(5000)"} {
		q.Add("_pragma", p)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	// One connection = one writer; parallel rips serialise their writes here.
	db.SetMaxOpenConns(1)

	s := &Store{db: db, path: path}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating %s: %w", path, err)
	}
	return s, nil
}

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// SchemaVersion returns the number of the last applied migration.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

type migration struct {
	version int
	name    string
}

// migrate applies migrations/NNNN_*.sql files newer than PRAGMA user_version,
// each in its own transaction.
func (s *Store) migrate(ctx context.Context) error {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	var ms []migration
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || !strings.HasSuffix(e.Name(), ".sql") {
			return fmt.Errorf("bad migration file name %q", e.Name())
		}
		ms = append(ms, migration{v, e.Name()})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })

	current, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if n := len(ms); n > 0 && current > ms[n-1].version {
		return fmt.Errorf("database schema version %d is newer than this blurip supports (%d); upgrade blurip", current, ms[n-1].version)
	}
	for _, m := range ms {
		if m.version <= current {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + m.name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// Setting returns a setting's value, or ErrNotFound.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// Settings returns all settings.
func (s *Store) Settings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetSettings writes several settings in one transaction.
func (s *Store) SetSettings(ctx context.Context, kv map[string]string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error { return setSettings(ctx, tx, kv) })
}

func setSettings(ctx context.Context, tx *sql.Tx, kv map[string]string) error {
	for k, v := range kv {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", k, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Drive is a configured optical drive.
type Drive struct {
	ID       int64
	Device   string // /dev/sr1
	Name     string // model, as MakeMKV reports it
	Nickname string // chosen by the user; "" if none
	Enabled  bool
}

// ShortDevice returns the device without /dev/ ("sr1").
func ShortDevice(dev string) string { return strings.TrimPrefix(dev, "/dev/") }

// Label is how the drive is shown: "Blu-ray drive (sr1)", or "sr1" if it
// has no nickname.
func (d Drive) Label() string {
	if d.Nickname == "" {
		return ShortDevice(d.Device)
	}
	return fmt.Sprintf("%s (%s)", d.Nickname, ShortDevice(d.Device))
}

// Matches reports whether ref names this drive: its device ("/dev/sr1" or
// "sr1") or its nickname (any case).
func (d Drive) Matches(ref string) bool {
	return ref == d.Device || ref == ShortDevice(d.Device) ||
		(d.Nickname != "" && strings.EqualFold(strings.TrimSpace(ref), d.Nickname))
}

// Drives returns all configured drives ordered by device.
func (s *Store) Drives(ctx context.Context) ([]Drive, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, device, name, nickname, enabled FROM drives ORDER BY device")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Drive
	for rows.Next() {
		var d Drive
		if err := rows.Scan(&d.ID, &d.Device, &d.Name, &d.Nickname, &d.Enabled); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ErrUnknownDrive means the device is not in the drives table.
var ErrUnknownDrive = errors.New("drive not configured")

// FindDrive returns the configured drive that ref names (see
// Drive.Matches), preferring enabled drives.
func (s *Store) FindDrive(ctx context.Context, ref string) (Drive, error) {
	drives, err := s.Drives(ctx)
	if err != nil {
		return Drive{}, err
	}
	var found *Drive
	for i, d := range drives {
		if d.Matches(ref) && (found == nil || (d.Enabled && !found.Enabled)) {
			found = &drives[i]
		}
	}
	if found == nil {
		return Drive{}, fmt.Errorf("%s: %w", ref, ErrUnknownDrive)
	}
	return *found, nil
}

// ErrNicknameTaken means another enabled drive already has the nickname.
var ErrNicknameTaken = errors.New("another drive already has that nickname")

// SetDriveNickname renames a configured drive. Nicknames are unique (any
// case) among enabled drives.
func (s *Store) SetDriveNickname(ctx context.Context, device, nickname string) error {
	nickname = strings.TrimSpace(nickname)
	drives, err := s.Drives(ctx)
	if err != nil {
		return err
	}
	for _, d := range drives {
		if d.Device != device && d.Enabled && nickname != "" && strings.EqualFold(d.Nickname, nickname) {
			return fmt.Errorf("%q: %w (%s)", nickname, ErrNicknameTaken, d.Device)
		}
	}
	res, err := s.db.ExecContext(ctx, "UPDATE drives SET nickname = ? WHERE device = ?", nickname, device)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("%s: %w", device, ErrUnknownDrive)
	}
	return nil
}

// SetDriveEnabled turns a configured drive on or off.
func (s *Store) SetDriveEnabled(ctx context.Context, device string, enabled bool) error {
	res, err := s.db.ExecContext(ctx, "UPDATE drives SET enabled = ? WHERE device = ?", enabled, device)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("%s: %w", device, ErrUnknownDrive)
	}
	return nil
}

// ReplaceDrives makes the given drives the enabled set. Drives not listed are
// kept (job history references them) but disabled.
func (s *Store) ReplaceDrives(ctx context.Context, drives []Drive) error {
	return s.inTx(ctx, func(tx *sql.Tx) error { return replaceDrives(ctx, tx, drives) })
}

// SaveSetup writes settings and the enabled drive set in one transaction.
func (s *Store) SaveSetup(ctx context.Context, kv map[string]string, drives []Drive) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := setSettings(ctx, tx, kv); err != nil {
			return err
		}
		return replaceDrives(ctx, tx, drives)
	})
}

func replaceDrives(ctx context.Context, tx *sql.Tx, drives []Drive) error {
	if _, err := tx.ExecContext(ctx, "UPDATE drives SET enabled = 0"); err != nil {
		return err
	}
	for _, d := range drives {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO drives(device, name, nickname, enabled) VALUES(?, ?, ?, 1)
			 ON CONFLICT(device) DO UPDATE SET name = excluded.name, nickname = excluded.nickname, enabled = 1`,
			d.Device, d.Name, strings.TrimSpace(d.Nickname)); err != nil {
			return err
		}
	}
	return nil
}
