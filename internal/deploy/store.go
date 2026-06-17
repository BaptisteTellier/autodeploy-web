package deploy

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // register the "sqlite" driver
)

// PersistedDeployment is the on-disk representation of a deployment record.
type PersistedDeployment struct {
	View  View
	Lines []string
}

// Store is a SQLite-backed persistence layer for deployment records.
type Store struct {
	db *sql.DB
}

// OpenStore opens (creating if needed) the SQLite deployment database at path
// and ensures the schema exists.
func OpenStore(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("deploy store open: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("deploy store ping: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("deploy store migrate: %w", err)
	}
	return s, nil
}

// migrate creates the deployments table if it does not yet exist.
func (s *Store) migrate() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS deployments (
		id           TEXT    PRIMARY KEY,
		kind         TEXT    NOT NULL,
		state        TEXT    NOT NULL,
		created_at   INTEGER NOT NULL,
		finished_at  INTEGER NOT NULL,
		error        TEXT    NOT NULL,
		has_wirer    INTEGER NOT NULL,
		nodes_json   TEXT    NOT NULL,
		form_json    TEXT    NOT NULL,
		log_json     TEXT    NOT NULL,
		retried_from TEXT    NOT NULL DEFAULT ''
	)`)
	return err
}

// Close closes the underlying database connection.
func (s *Store) Close() error { return s.db.Close() }

// Save upserts a deployment record (keyed by id).
func (s *Store) Save(rec PersistedDeployment) error {
	v := rec.View

	nodesJSON, err := json.Marshal(v.Nodes)
	if err != nil {
		return fmt.Errorf("deploy store marshal nodes: %w", err)
	}
	formJSON, err := json.Marshal(v.Form)
	if err != nil {
		return fmt.Errorf("deploy store marshal form: %w", err)
	}
	logJSON, err := json.Marshal(rec.Lines)
	if err != nil {
		return fmt.Errorf("deploy store marshal lines: %w", err)
	}

	hasWirer := 0
	if v.HasWirer {
		hasWirer = 1
	}

	_, err = s.db.Exec(`INSERT INTO deployments
		(id, kind, state, created_at, finished_at, error, has_wirer, nodes_json, form_json, log_json, retried_from)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
		  kind         = excluded.kind,
		  state        = excluded.state,
		  created_at   = excluded.created_at,
		  finished_at  = excluded.finished_at,
		  error        = excluded.error,
		  has_wirer    = excluded.has_wirer,
		  nodes_json   = excluded.nodes_json,
		  form_json    = excluded.form_json,
		  log_json     = excluded.log_json,
		  retried_from = excluded.retried_from`,
		v.ID,
		v.Kind,
		string(v.State),
		toDeployNanos(v.CreatedAt),
		toDeployNanos(v.FinishedAt),
		v.Error,
		hasWirer,
		string(nodesJSON),
		string(formJSON),
		string(logJSON),
		v.RetriedFrom,
	)
	if err != nil {
		return fmt.Errorf("deploy store save %s: %w", v.ID, err)
	}
	return nil
}

// LoadAll returns all persisted deployment records ordered by created_at ascending.
func (s *Store) LoadAll() ([]PersistedDeployment, error) {
	rows, err := s.db.Query(`SELECT
		id, kind, state, created_at, finished_at, error, has_wirer, nodes_json, form_json, log_json, retried_from
		FROM deployments ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("deploy store load: %w", err)
	}
	defer rows.Close()

	var out []PersistedDeployment
	for rows.Next() {
		var (
			v           View
			state       string
			createdAt   int64
			finishedAt  int64
			hasWirer    int
			nodesJSON   string
			formJSON    string
			logJSON     string
			retriedFrom string
		)
		if err := rows.Scan(
			&v.ID, &v.Kind, &state, &createdAt, &finishedAt,
			&v.Error, &hasWirer, &nodesJSON, &formJSON, &logJSON, &retriedFrom,
		); err != nil {
			return nil, fmt.Errorf("deploy store scan: %w", err)
		}
		v.State = State(state)
		v.CreatedAt = fromDeployNanos(createdAt)
		v.FinishedAt = fromDeployNanos(finishedAt)
		v.HasWirer = hasWirer != 0
		v.RetriedFrom = retriedFrom

		if err := json.Unmarshal([]byte(nodesJSON), &v.Nodes); err != nil {
			return nil, fmt.Errorf("deploy store unmarshal nodes %s: %w", v.ID, err)
		}
		if err := json.Unmarshal([]byte(formJSON), &v.Form); err != nil {
			return nil, fmt.Errorf("deploy store unmarshal form %s: %w", v.ID, err)
		}
		var lines []string
		if err := json.Unmarshal([]byte(logJSON), &lines); err != nil {
			return nil, fmt.Errorf("deploy store unmarshal log %s: %w", v.ID, err)
		}
		out = append(out, PersistedDeployment{View: v, Lines: lines})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("deploy store rows: %w", err)
	}
	return out, nil
}

// Delete removes a deployment record by id.
func (s *Store) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM deployments WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deploy store delete %s: %w", id, err)
	}
	return nil
}

// toDeployNanos converts a time.Time to Unix nanoseconds (0 for zero time).
func toDeployNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

// fromDeployNanos converts Unix nanoseconds to time.Time (zero time when n == 0).
func fromDeployNanos(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}
