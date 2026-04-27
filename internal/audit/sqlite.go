package audit

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
)

func openUsageDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	if err := migrateUsageDB(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func migrateUsageDB(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS usage_records (
			request_id TEXT PRIMARY KEY,
			created_at TEXT NOT NULL,
			prefix_hash TEXT,
			model TEXT,
			stream INTEGER NOT NULL DEFAULT 0,
			upstream_status INTEGER NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			customer_original_input_tokens INTEGER NOT NULL DEFAULT 0,
			original_estimated_input_tokens INTEGER NOT NULL DEFAULT 0,
			forward_estimated_input_tokens INTEGER NOT NULL DEFAULT 0,
			saved_input_tokens INTEGER NOT NULL DEFAULT 0,
			estimated_saved_input_tokens INTEGER NOT NULL DEFAULT 0,
			saved_input_token_percent REAL NOT NULL DEFAULT 0,
			upstream_input_tokens INTEGER NOT NULL DEFAULT 0,
			upstream_output_tokens INTEGER NOT NULL DEFAULT 0,
			cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_input_tokens INTEGER NOT NULL DEFAULT 0,
			checkpoint_hit INTEGER NOT NULL DEFAULT 0,
			checkpoint_coverage_end_index INTEGER NOT NULL DEFAULT 0,
			checkpoint_compressor TEXT,
			checkpoint_compressor_model TEXT,
			passthrough_reason TEXT,
			error_class TEXT
		)`,
		`DROP VIEW IF EXISTS usage_summary`,
		`CREATE INDEX IF NOT EXISTS usage_records_created_at_idx ON usage_records(created_at)`,
		`CREATE INDEX IF NOT EXISTS usage_records_model_idx ON usage_records(model)`,
		`CREATE INDEX IF NOT EXISTS usage_records_checkpoint_hit_idx ON usage_records(checkpoint_hit)`,
	}

	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}

	if err := ensureUsageColumns(db); err != nil {
		return err
	}

	statements = []string{
		`CREATE VIEW IF NOT EXISTS usage_summary AS
			SELECT
				COUNT(*) AS request_count,
				SUM(CASE WHEN checkpoint_hit = 1 THEN 1 ELSE 0 END) AS checkpoint_hit_count,
				SUM(customer_original_input_tokens) AS customer_original_input_tokens,
				SUM(original_estimated_input_tokens) AS original_estimated_input_tokens,
				SUM(forward_estimated_input_tokens) AS forward_estimated_input_tokens,
				SUM(saved_input_tokens) AS saved_input_tokens,
				SUM(estimated_saved_input_tokens) AS estimated_saved_input_tokens,
				CASE
					WHEN SUM(customer_original_input_tokens) > 0
					THEN ROUND(100.0 * SUM(saved_input_tokens) / SUM(customer_original_input_tokens), 4)
					ELSE 0
				END AS saved_input_token_percent,
				SUM(upstream_input_tokens) AS upstream_input_tokens,
				SUM(upstream_output_tokens) AS upstream_output_tokens,
				SUM(cache_creation_input_tokens) AS cache_creation_input_tokens,
				SUM(cache_read_input_tokens) AS cache_read_input_tokens
			FROM usage_records`,
	}

	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func ensureUsageColumns(db *sql.DB) error {
	columns := map[string]string{
		"customer_original_input_tokens": "INTEGER NOT NULL DEFAULT 0",
		"saved_input_tokens":             "INTEGER NOT NULL DEFAULT 0",
		"saved_input_token_percent":      "REAL NOT NULL DEFAULT 0",
		"checkpoint_compressor":          "TEXT",
		"checkpoint_compressor_model":    "TEXT",
	}

	rows, err := db.Query(`PRAGMA table_info(usage_records)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	existing := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for name, definition := range columns {
		if existing[name] {
			continue
		}
		statement := "ALTER TABLE usage_records ADD COLUMN " + name + " " + definition
		if _, err := db.Exec(statement); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return err
			}
		}
	}
	return nil
}

func insertUsageRecord(db *sql.DB, record Record) error {
	_, err := db.Exec(
		`INSERT INTO usage_records (
			request_id,
			created_at,
			prefix_hash,
			model,
			stream,
			upstream_status,
			duration_ms,
			customer_original_input_tokens,
			original_estimated_input_tokens,
			forward_estimated_input_tokens,
			saved_input_tokens,
			estimated_saved_input_tokens,
			saved_input_token_percent,
			upstream_input_tokens,
			upstream_output_tokens,
			cache_creation_input_tokens,
			cache_read_input_tokens,
			checkpoint_hit,
			checkpoint_coverage_end_index,
			checkpoint_compressor,
			checkpoint_compressor_model,
			passthrough_reason,
			error_class
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(request_id) DO UPDATE SET
			created_at = excluded.created_at,
			prefix_hash = excluded.prefix_hash,
			model = excluded.model,
			stream = excluded.stream,
			upstream_status = excluded.upstream_status,
			duration_ms = excluded.duration_ms,
			customer_original_input_tokens = excluded.customer_original_input_tokens,
			original_estimated_input_tokens = excluded.original_estimated_input_tokens,
			forward_estimated_input_tokens = excluded.forward_estimated_input_tokens,
			saved_input_tokens = excluded.saved_input_tokens,
			estimated_saved_input_tokens = excluded.estimated_saved_input_tokens,
			saved_input_token_percent = excluded.saved_input_token_percent,
			upstream_input_tokens = excluded.upstream_input_tokens,
			upstream_output_tokens = excluded.upstream_output_tokens,
			cache_creation_input_tokens = excluded.cache_creation_input_tokens,
			cache_read_input_tokens = excluded.cache_read_input_tokens,
			checkpoint_hit = excluded.checkpoint_hit,
			checkpoint_coverage_end_index = excluded.checkpoint_coverage_end_index,
			checkpoint_compressor = excluded.checkpoint_compressor,
			checkpoint_compressor_model = excluded.checkpoint_compressor_model,
			passthrough_reason = excluded.passthrough_reason,
			error_class = excluded.error_class`,
		record.RequestID,
		record.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
		record.PrefixHash,
		record.Model,
		boolInt(record.Stream),
		record.UpstreamStatus,
		record.DurationMs,
		record.CustomerOriginalInputTokens,
		record.OriginalEstimatedInputTokens,
		record.ForwardEstimatedInputTokens,
		record.SavedInputTokens,
		record.EstimatedSavedInputTokens,
		record.SavedInputTokenPercent,
		record.InputTokens,
		record.OutputTokens,
		record.CacheCreationInputTokens,
		record.CacheReadInputTokens,
		boolInt(record.CheckpointHit),
		record.CheckpointCoverageEndIndex,
		record.CheckpointCompressor,
		record.CheckpointCompressorModel,
		record.PassthroughReason,
		record.ErrorClass,
	)
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
