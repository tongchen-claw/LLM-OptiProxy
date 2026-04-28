package audit

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type SavingsRecord struct {
	RequestID                    string    `json:"request_id"`
	PrefixHash                   string    `json:"prefix_hash,omitempty"`
	Model                        string    `json:"model,omitempty"`
	UpstreamStatus               int       `json:"upstream_status,omitempty"`
	DurationMs                   int64     `json:"duration_ms"`
	OriginalEstimatedInputTokens int       `json:"original_estimated_input_tokens"`
	ForwardEstimatedInputTokens  int       `json:"forward_estimated_input_tokens"`
	SavedInputTokens             int       `json:"saved_input_tokens"`
	EstimatedSavedInputTokens    int       `json:"estimated_saved_input_tokens"`
	SavedInputTokenPercent       float64   `json:"saved_input_token_percent"`
	CheckpointHit                bool      `json:"checkpoint_hit"`
	CheckpointCoverageEndIndex   int       `json:"checkpoint_coverage_end_index"`
	CheckpointCompressor         string    `json:"checkpoint_compressor,omitempty"`
	CheckpointCompressorModel    string    `json:"checkpoint_compressor_model,omitempty"`
	CreatedAt                    time.Time `json:"created_at"`
}

type SavingsLogger struct {
	mu     sync.Mutex
	writer io.WriteCloser
}

func NewSavingsLogger(path string) (*SavingsLogger, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return &SavingsLogger{}, nil
	}
	if path == "-" {
		return &SavingsLogger{writer: nopCloser{Writer: os.Stdout}}, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &SavingsLogger{writer: file}, nil
}

func (l *SavingsLogger) Log(record Record) {
	if l == nil || l.writer == nil {
		return
	}
	if !record.CheckpointHit || record.SavedInputTokens <= 0 {
		return
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}

	savings := SavingsRecord{
		RequestID:                    record.RequestID,
		PrefixHash:                   record.PrefixHash,
		Model:                        record.Model,
		UpstreamStatus:               record.UpstreamStatus,
		DurationMs:                   record.DurationMs,
		OriginalEstimatedInputTokens: record.OriginalEstimatedInputTokens,
		ForwardEstimatedInputTokens:  record.ForwardEstimatedInputTokens,
		SavedInputTokens:             record.SavedInputTokens,
		EstimatedSavedInputTokens:    record.EstimatedSavedInputTokens,
		SavedInputTokenPercent:       record.SavedInputTokenPercent,
		CheckpointHit:                record.CheckpointHit,
		CheckpointCoverageEndIndex:   record.CheckpointCoverageEndIndex,
		CheckpointCompressor:         record.CheckpointCompressor,
		CheckpointCompressorModel:    record.CheckpointCompressorModel,
		CreatedAt:                    record.CreatedAt,
	}

	raw, err := json.Marshal(savings)
	if err != nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.writer.Write(append(raw, '\n'))
}

func (l *SavingsLogger) Close() error {
	if l == nil || l.writer == nil {
		return nil
	}
	return l.writer.Close()
}
