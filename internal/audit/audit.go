package audit

import (
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Usage struct {
	InputTokens              int `json:"input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

type Record struct {
	RequestID                    string    `json:"request_id"`
	PrefixHash                   string    `json:"prefix_hash,omitempty"`
	Model                        string    `json:"model,omitempty"`
	Stream                       bool      `json:"stream"`
	UpstreamStatus               int       `json:"upstream_status,omitempty"`
	DurationMs                   int64     `json:"duration_ms"`
	CustomerOriginalInputTokens  int       `json:"customer_original_input_tokens,omitempty"`
	OriginalEstimatedInputTokens int       `json:"original_estimated_input_tokens,omitempty"`
	ForwardEstimatedInputTokens  int       `json:"forward_estimated_input_tokens,omitempty"`
	SavedInputTokens             int       `json:"saved_input_tokens,omitempty"`
	EstimatedSavedInputTokens    int       `json:"estimated_saved_input_tokens,omitempty"`
	SavedInputTokenPercent       float64   `json:"saved_input_token_percent,omitempty"`
	InputTokens                  int       `json:"input_tokens,omitempty"`
	OutputTokens                 int       `json:"output_tokens,omitempty"`
	CacheCreationInputTokens     int       `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens         int       `json:"cache_read_input_tokens,omitempty"`
	CheckpointHit                bool      `json:"checkpoint_hit"`
	CheckpointCoverageEndIndex   int       `json:"checkpoint_coverage_end_index,omitempty"`
	CheckpointCompressor         string    `json:"checkpoint_compressor,omitempty"`
	CheckpointCompressorModel    string    `json:"checkpoint_compressor_model,omitempty"`
	PassthroughReason            string    `json:"passthrough_reason,omitempty"`
	ErrorClass                   string    `json:"error_class,omitempty"`
	CreatedAt                    time.Time `json:"created_at"`
}

type Logger struct {
	enabled bool
	mu      sync.Mutex
	writer  io.WriteCloser
	db      *sql.DB
}

func NewLogger(path string, sqlitePath string, enabled bool) (*Logger, error) {
	logger := &Logger{enabled: enabled}
	if !enabled {
		return logger, nil
	}
	if path == "" || path == "-" {
		logger.writer = nopCloser{Writer: os.Stdout}
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		logger.writer = file
	}

	if sqlitePath != "" && sqlitePath != "-" {
		db, err := openUsageDB(sqlitePath)
		if err != nil {
			if logger.writer != nil {
				_ = logger.writer.Close()
			}
			return nil, err
		}
		logger.db = db
	}

	return logger, nil
}

func (l *Logger) Log(record Record) {
	if l == nil || !l.enabled {
		return
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}

	raw, err := json.Marshal(record)
	if err != nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.writer != nil {
		_, _ = l.writer.Write(append(raw, '\n'))
	}
	if l.db != nil {
		if err := insertUsageRecord(l.db, record); err != nil {
			log.Printf("sqlite usage insert failed request_id=%s: %v", record.RequestID, err)
		}
	}
}

func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	var err error
	if l.writer != nil {
		err = l.writer.Close()
	}
	if l.db != nil {
		if dbErr := l.db.Close(); err == nil {
			err = dbErr
		}
	}
	return err
}

type nopCloser struct {
	io.Writer
}

func (n nopCloser) Close() error {
	return nil
}

func ExtractUsage(raw []byte) Usage {
	var payload struct {
		Usage   Usage `json:"usage"`
		Message struct {
			Usage Usage `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Usage{}
	}
	if payload.Usage != (Usage{}) {
		return payload.Usage
	}
	return payload.Message.Usage
}

func MergeUsage(base, next Usage) Usage {
	if next.InputTokens != 0 {
		base.InputTokens = next.InputTokens
	}
	if next.OutputTokens != 0 {
		base.OutputTokens = next.OutputTokens
	}
	if next.CacheCreationInputTokens != 0 {
		base.CacheCreationInputTokens = next.CacheCreationInputTokens
	}
	if next.CacheReadInputTokens != 0 {
		base.CacheReadInputTokens = next.CacheReadInputTokens
	}
	return base
}
