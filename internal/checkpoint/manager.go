package checkpoint

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/tongchen-claw/LLM-OptiProxy/internal/anthropic"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/canonicalize"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/config"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/store"
)

type Manager struct {
	cfg        config.Config
	store      store.Store
	compressor Compressor
	jobs       chan buildJob
}

type ApplyResult struct {
	Body              []byte
	CheckpointHit     bool
	CoverageEndIndex  int
	CompressorMode    string
	CompressorModel   string
	PassthroughReason string
}

type buildJob struct {
	request  *anthropic.Request
	analysis canonicalize.Analysis
	record   store.CheckpointRecord
}

func NewManager(cfg config.Config, state store.Store) *Manager {
	workers := cfg.CheckpointAsyncWorkers
	if workers <= 0 {
		workers = 1
	}
	return &Manager{
		cfg:        cfg,
		store:      state,
		compressor: newCompressor(cfg),
		jobs:       make(chan buildJob, workers*4),
	}
}

func (m *Manager) Start(ctx context.Context) {
	workers := m.cfg.CheckpointAsyncWorkers
	if workers <= 0 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		go m.worker(ctx)
	}
}

func (m *Manager) Apply(req *anthropic.Request, analysis canonicalize.Analysis) ApplyResult {
	if req == nil {
		return ApplyResult{PassthroughReason: "request_parse_failed"}
	}
	if !m.cfg.EnableCheckpointing {
		return ApplyResult{PassthroughReason: "checkpoint_disabled"}
	}
	if !m.compressorEnabled() {
		return ApplyResult{PassthroughReason: "checkpoint_compressor_unsupported"}
	}
	if len(analysis.Candidates) == 0 {
		return ApplyResult{PassthroughReason: "no_compressible_prefix"}
	}

	record, ok := m.store.FindReadyCheckpoint(analysis.Candidates, m.cfg.KeepRecentTurns, m.cfg.CheckpointCompressor, m.cfg.CompressorModel)
	if !ok {
		return ApplyResult{PassthroughReason: "checkpoint_miss"}
	}

	messages := make([]anthropic.Message, 0, 1+len(req.Messages)-record.CoverageEndIndex)
	messages = append(messages, anthropic.NewTextMessage("assistant", record.SummaryText))
	messages = append(messages, req.Messages[record.CoverageEndIndex:]...)

	body, err := req.MarshalWithMessages(messages)
	if err != nil {
		return ApplyResult{PassthroughReason: "checkpoint_rewrite_failed"}
	}

	return ApplyResult{
		Body:             body,
		CheckpointHit:    true,
		CoverageEndIndex: record.CoverageEndIndex,
		CompressorMode:   record.CompressorMode,
		CompressorModel:  record.CompressorModel,
	}
}

func (m *Manager) Schedule(req *anthropic.Request, analysis canonicalize.Analysis) {
	if req == nil || !m.cfg.EnableCheckpointing || !m.compressorEnabled() {
		return
	}
	if !m.shouldBuild(analysis) {
		return
	}
	if m.store.HasBuildingCheckpoint(analysis.Candidates, m.cfg.KeepRecentTurns, m.cfg.CheckpointCompressor, m.cfg.CompressorModel) {
		return
	}

	record := store.CheckpointRecord{
		PrefixHash:       analysis.CoverageHash,
		CoverageEndIndex: analysis.CoverageEnd,
		KeepRecentTurns:  m.cfg.KeepRecentTurns,
		SummaryFormat:    "conversation-checkpoint-v1",
		CompressorMode:   m.cfg.CheckpointCompressor,
		CompressorModel:  m.cfg.CompressorModel,
		CreatedAt:        time.Now().UTC(),
	}

	if !m.store.TryStartCheckpoint(record) {
		return
	}

	job := buildJob{
		request:  req,
		analysis: analysis,
		record:   record,
	}

	select {
	case m.jobs <- job:
	default:
		m.store.FailCheckpoint(record, "checkpoint_queue_full")
	}
}

func (m *Manager) shouldBuild(analysis canonicalize.Analysis) bool {
	if analysis.CoverageEnd <= 0 || analysis.CoverageHash == "" {
		return false
	}
	if analysis.EstimatedTokens < m.cfg.TokenSoftLimit && analysis.MessageCount < m.cfg.MessageCountThreshold {
		return false
	}
	return true
}

func (m *Manager) compressorEnabled() bool {
	return m.compressor != nil
}

func (m *Manager) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-m.jobs:
			m.runJob(ctx, job)
		}
	}
}

func (m *Manager) runJob(ctx context.Context, job buildJob) {
	summary, err := m.compressor.Compress(ctx, job.request, job.analysis.CoverageEnd)
	if err != nil {
		errorClass := "checkpoint_compressor_failed"
		if errors.Is(err, context.Canceled) {
			errorClass = "checkpoint_context_canceled"
		}
		m.store.FailCheckpoint(job.record, errorClass)
		log.Printf("checkpoint build failed prefix=%s: %v", job.record.PrefixHash, err)
		return
	}

	record := job.record
	record.SummaryText = summary
	m.store.CompleteCheckpoint(record)
}

type Compressor interface {
	Compress(ctx context.Context, req *anthropic.Request, coverageEnd int) (string, error)
}

func newCompressor(cfg config.Config) Compressor {
	switch strings.ToLower(strings.TrimSpace(cfg.CheckpointCompressor)) {
	case "", "local", "local-extractive":
		return LocalExtractiveCompressor{MaxSummaryChars: cfg.MaxSummaryChars}
	case "openai", "openai-chat", "chat-completions":
		if cfg.CompressorBaseURL == "" || cfg.CompressorModel == "" || cfg.CompressorAPIKey == "" {
			log.Printf("checkpoint compressor openai-chat disabled: base URL, model, and API key are required")
			return nil
		}
		return NewOpenAIChatCompressor(OpenAIChatConfig{
			BaseURL:         cfg.CompressorBaseURL,
			Model:           cfg.CompressorModel,
			APIKey:          cfg.CompressorAPIKey,
			MaxSummaryChars: cfg.MaxSummaryChars,
		})
	case "disabled":
		return nil
	default:
		log.Printf("checkpoint compressor disabled: unsupported mode %q", cfg.CheckpointCompressor)
		return nil
	}
}

type LocalExtractiveCompressor struct {
	MaxSummaryChars int
}

func (c LocalExtractiveCompressor) Compress(ctx context.Context, req *anthropic.Request, coverageEnd int) (string, error) {
	if req == nil {
		return "", errors.New("nil request")
	}
	if coverageEnd <= 0 || coverageEnd > len(req.Messages) {
		return "", errors.New("invalid coverage end")
	}

	limit := c.MaxSummaryChars
	if limit <= 0 {
		limit = 6000
	}

	var b strings.Builder
	b.WriteString("# Conversation Checkpoint\n\n")
	b.WriteString("This assistant checkpoint summarizes older conversation state. It is not a new user request.\n\n")
	b.WriteString("## Stable constraints\n")
	b.WriteString("- Preserve explicit user constraints from the summarized history when they remain relevant.\n\n")
	b.WriteString("## Project state\n")
	b.WriteString("- Older conversation messages were summarized locally by the proxy.\n\n")
	b.WriteString("## Open work\n")
	b.WriteString("- Continue from the uncompressed recent turns that follow this checkpoint.\n\n")
	b.WriteString("## Tool and error history\n")

	for i, message := range req.Messages[:coverageEnd] {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		snippet := strings.TrimSpace(message.TextSnippet(500))
		if snippet == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(message.Role)
		b.WriteString(" #")
		b.WriteString(intString(i + 1))
		b.WriteString(": ")
		b.WriteString(strings.ReplaceAll(snippet, "\n", " "))
		b.WriteString("\n")
		if b.Len() >= limit {
			break
		}
	}

	summary := b.String()
	if len(summary) > limit {
		return summary[:limit], nil
	}
	return summary, nil
}

func intString(value int) string {
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[i:])
}
