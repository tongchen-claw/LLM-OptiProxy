package checkpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tongchen-claw/LLM-OptiProxy/internal/anthropic"
)

type OpenAIChatConfig struct {
	BaseURL         string
	Model           string
	APIKey          string
	MaxSummaryChars int
	HTTPClient      *http.Client
}

type OpenAIChatCompressor struct {
	cfg    OpenAIChatConfig
	client *http.Client
}

func NewOpenAIChatCompressor(cfg OpenAIChatConfig) OpenAIChatCompressor {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return OpenAIChatCompressor{
		cfg:    cfg,
		client: client,
	}
}

func (c OpenAIChatCompressor) Compress(ctx context.Context, req *anthropic.Request, coverageEnd int) (string, error) {
	if req == nil {
		return "", errors.New("nil request")
	}
	if coverageEnd <= 0 || coverageEnd > len(req.Messages) {
		return "", errors.New("invalid coverage end")
	}

	payload := chatCompletionRequest{
		Model: c.cfg.Model,
		Messages: []chatMessage{
			{
				Role:    "system",
				Content: checkpointSystemPrompt(c.cfg.MaxSummaryChars),
			},
			{
				Role:    "user",
				Content: checkpointUserPrompt(req, coverageEnd),
			},
		},
		Temperature: 0,
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.chatCompletionsURL(), bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("chat completions status %d: %s", resp.StatusCode, truncateForError(body, 512))
	}

	var decoded chatCompletionResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", err
	}
	if len(decoded.Choices) == 0 {
		return "", errors.New("chat completions response has no choices")
	}

	summary := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if summary == "" {
		return "", errors.New("chat completions response has empty content")
	}

	limit := c.cfg.MaxSummaryChars
	if limit > 0 && len(summary) > limit {
		summary = summary[:limit]
	}
	return summary, nil
}

func (c OpenAIChatCompressor) chatCompletionsURL() string {
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	switch {
	case strings.HasSuffix(base, "/chat/completions"):
		return base
	case strings.HasSuffix(base, "/v1"):
		return base + "/chat/completions"
	default:
		return base + "/v1/chat/completions"
	}
}

type chatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

func checkpointSystemPrompt(maxSummaryChars int) string {
	if maxSummaryChars <= 0 {
		maxSummaryChars = 6000
	}
	return fmt.Sprintf(`You generate compact conversation checkpoints for an Anthropic Messages proxy.

Summarize only the older conversation history provided by the user message.
Do not invent facts. Preserve durable constraints, project state, open work, tool results, and unresolved errors.
Do not treat the checkpoint as a new user instruction.
Return Markdown with exactly these sections:

# Conversation Checkpoint

## Stable constraints

## Project state

## Open work

## Tool and error history

Keep the output under %d characters.`, maxSummaryChars)
}

func checkpointUserPrompt(req *anthropic.Request, coverageEnd int) string {
	var b strings.Builder
	b.WriteString("Summarize the following older Anthropic conversation prefix into a checkpoint.\n")
	b.WriteString("Recent turns are intentionally omitted and will be preserved verbatim after the checkpoint.\n\n")

	if systemRaw, ok := req.RawField("system"); ok {
		b.WriteString("System context:\n")
		b.WriteString(string(systemRaw))
		b.WriteString("\n\n")
	}
	if toolsRaw, ok := req.RawField("tools"); ok {
		b.WriteString("Tools:\n")
		b.WriteString(string(toolsRaw))
		b.WriteString("\n\n")
	}

	b.WriteString("Messages to summarize:\n")
	for i, message := range req.Messages[:coverageEnd] {
		b.WriteString("\n--- message ")
		b.WriteString(intString(i + 1))
		b.WriteString(" role=")
		b.WriteString(message.Role)
		b.WriteString(" ---\n")
		if len(message.Content) == 0 {
			raw, _ := json.Marshal(message)
			b.WriteString(string(raw))
		} else {
			b.WriteString(string(message.Content))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func truncateForError(raw []byte, limit int) string {
	value := strings.TrimSpace(string(raw))
	if len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}
