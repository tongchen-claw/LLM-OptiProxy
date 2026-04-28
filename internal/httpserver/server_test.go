package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tongchen-claw/LLM-OptiProxy/internal/anthropic"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/audit"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/canonicalize"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/checkpoint"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/config"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/store"
)

func TestHandleMessagesLogsSavingsWhenCheckpointRewritesRequest(t *testing.T) {
	var forwardedMessageCount int
	var forwardedFirstRole string
	var forwardedFirstContent string

	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}

		var payload struct {
			Messages []anthropic.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode forwarded request: %v", err)
		}
		forwardedMessageCount = len(payload.Messages)
		if forwardedMessageCount > 0 {
			forwardedFirstRole = payload.Messages[0].Role
			_ = json.Unmarshal(payload.Messages[0].Content, &forwardedFirstContent)
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"msg_test","type":"message","role":"assistant","content":[],"usage":{"input_tokens":12,"output_tokens":3}}`)),
		}, nil
	})

	req := checkpointableRequest(t)
	analysis := canonicalize.Analyze(req, 1)

	state := store.NewMemoryStore()
	state.CompleteCheckpoint(store.CheckpointRecord{
		PrefixHash:       analysis.CoverageHash,
		CoverageEndIndex: analysis.CoverageEnd,
		KeepRecentTurns:  1,
		SummaryFormat:    "conversation-checkpoint-v1",
		SummaryText:      "short local checkpoint",
		CompressorMode:   "local-extractive",
		BuildStatus:      "ready",
	})

	savingsLogPath := filepath.Join(t.TempDir(), "savings.jsonl")
	savingsLog, err := audit.NewSavingsLogger(savingsLogPath)
	if err != nil {
		t.Fatalf("create savings logger: %v", err)
	}
	defer savingsLog.Close()

	cfg := config.Config{
		UpstreamBaseURL:       "http://upstream.test",
		EnableCheckpointing:   true,
		CheckpointCompressor:  "local-extractive",
		KeepRecentTurns:       1,
		MessageCountThreshold: 4,
		MaxSummaryChars:       6000,
	}

	server := &Server{
		cfg:         cfg,
		store:       state,
		checkpoints: checkpoint.NewManager(cfg, state),
		savingsLog:  savingsLog,
		client:      &http.Client{Transport: transport},
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(req.Body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	server.handleMessages(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	if forwardedMessageCount != 2 {
		t.Fatalf("forwarded message count = %d, want 2", forwardedMessageCount)
	}
	if forwardedFirstRole != "assistant" {
		t.Fatalf("forwarded first role = %q, want assistant", forwardedFirstRole)
	}
	if forwardedFirstContent != "short local checkpoint" {
		t.Fatalf("forwarded first content = %q", forwardedFirstContent)
	}

	rawLog, err := os.ReadFile(savingsLogPath)
	if err != nil {
		t.Fatalf("read savings log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(rawLog)), "\n")
	if len(lines) != 1 {
		t.Fatalf("savings log line count = %d, want 1; raw=%q", len(lines), rawLog)
	}

	var record audit.SavingsRecord
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("decode savings log: %v", err)
	}
	if !record.CheckpointHit {
		t.Fatal("savings log checkpoint_hit = false, want true")
	}
	if record.SavedInputTokens <= 0 {
		t.Fatalf("saved_input_tokens = %d, want > 0", record.SavedInputTokens)
	}
	if record.ForwardEstimatedInputTokens >= record.OriginalEstimatedInputTokens {
		t.Fatalf("forward tokens %d should be less than original tokens %d", record.ForwardEstimatedInputTokens, record.OriginalEstimatedInputTokens)
	}
	if record.CheckpointCoverageEndIndex != analysis.CoverageEnd {
		t.Fatalf("coverage end = %d, want %d", record.CheckpointCoverageEndIndex, analysis.CoverageEnd)
	}
}

func checkpointableRequest(t *testing.T) *anthropic.Request {
	t.Helper()

	longContext := strings.Repeat("stable project context and user constraints ", 80)
	body := `{
		"model":"claude-test",
		"max_tokens":128,
		"messages":[
			{"role":"user","content":"` + longContext + `first user turn"},
			{"role":"assistant","content":"` + longContext + `first assistant turn"},
			{"role":"user","content":"` + longContext + `second user turn"},
			{"role":"assistant","content":"` + longContext + `second assistant turn"},
			{"role":"user","content":"continue from the latest state"}
		]
	}`

	req, err := anthropic.ParseRequest([]byte(body))
	if err != nil {
		t.Fatalf("parse request fixture: %v", err)
	}
	return req
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}
