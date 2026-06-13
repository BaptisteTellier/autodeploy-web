package job

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // register the "sqlite" driver
)

// PersistedJob is the on-disk representation of a job record.
type PersistedJob struct {
	View       JobView
	ConfigPath string
}

// Store is a SQLite-backed persistence layer for job records.
type Store struct {
	db *sql.DB
}

// OpenStore opens (creating if needed) the SQLite job database at path and
// ensures the schema exists.
func OpenStore(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("job store open: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("job store ping: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("job store migrate: %w", err)
	}
	return s, nil
}

// migrate creates the jobs table if it does not yet exist.
func (s *Store) migrate() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS jobs (
		id          TEXT    PRIMARY KEY,
		state       TEXT    NOT NULL,
		hostname    TEXT    NOT NULL,
		appliance   TEXT    NOT NULL,
		source_iso  TEXT    NOT NULL,
		output_iso  TEXT    NOT NULL,
		config_path TEXT    NOT NULL,
		created_at  INTEGER NOT NULL,
		started_at  INTEGER NOT NULL,
		finished_at INTEGER NOT NULL,
		exit_code   INTEGER NOT NULL,
		error       TEXT    NOT NULL
	)`)
	return err
}

// Close closes the underlying database connection.
func (s *Store) Close() error { return s.db.Close() }

// SaveJob upserts a job record (keyed by id).
func (s *Store) SaveJob(v JobView, configPath string) error {
	_, err := s.db.Exec(`INSERT INTO jobs
		(id, state, hostname, appliance, source_iso, output_iso, config_path,
		 created_at, started_at, finished_at, exit_code, error)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
		  state       = excluded.state,
		  started_at  = excluded.started_at,
		  finished_at = excluded.finished_at,
		  exit_code   = excluded.exit_code,
		  error       = excluded.error`,
		v.ID,
		string(v.State),
		v.Hostname,
		v.Appliance,
		v.SourceISO,
		v.OutputISO,
		configPath,
		toNanos(v.CreatedAt),
		toNanos(v.StartedAt),
		toNanos(v.FinishedAt),
		v.ExitCode,
		v.ErrorMessage,
	)
	if err != nil {
		return fmt.Errorf("job store save %s: %w", v.ID, err)
	}
	return nil
}

// LoadJobs returns all persisted job records ordered by created_at ascending.
func (s *Store) LoadJobs() ([]PersistedJob, error) {
	rows, err := s.db.Query(`SELECT
		id, state, hostname, appliance, source_iso, output_iso, config_path,
		created_at, started_at, finished_at, exit_code, error
		FROM jobs ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("job store load: %w", err)
	}
	defer rows.Close()

	var out []PersistedJob
	for rows.Next() {
		var (
			v          JobView
			configPath string
			createdAt  int64
			startedAt  int64
			finishedAt int64
			state      string
		)
		if err := rows.Scan(
			&v.ID, &state, &v.Hostname, &v.Appliance,
			&v.SourceISO, &v.OutputISO, &configPath,
			&createdAt, &startedAt, &finishedAt,
			&v.ExitCode, &v.ErrorMessage,
		); err != nil {
			return nil, fmt.Errorf("job store scan: %w", err)
		}
		v.State = State(state)
		v.CreatedAt = fromNanos(createdAt)
		v.StartedAt = fromNanos(startedAt)
		v.FinishedAt = fromNanos(finishedAt)
		v.ConfigPath = configPath
		out = append(out, PersistedJob{View: v, ConfigPath: configPath})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("job store rows: %w", err)
	}
	return out, nil
}

// DeleteJob removes a job record by id.
func (s *Store) DeleteJob(id string) error {
	_, err := s.db.Exec(`DELETE FROM jobs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("job store delete %s: %w", id, err)
	}
	return nil
}

// toNanos converts a time.Time to Unix nanoseconds (0 for zero time).
func toNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

// fromNanos converts Unix nanoseconds to time.Time (zero time when n == 0).
func fromNanos(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}
