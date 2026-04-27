package canonicalize

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/tongchen-claw/LLM-OptiProxy/internal/anthropic"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/store"
)

type Analysis struct {
	PrefixHash      string
	RequestHash     string
	MessageCount    int
	EstimatedTokens int
	CoverageEnd     int
	CoverageHash    string
	Candidates      []store.CheckpointCandidate
}

func Analyze(req *anthropic.Request, keepRecentTurns int) Analysis {
	if req == nil {
		return Analysis{}
	}

	coverageEnd := RecentTurnStart(req.Messages, keepRecentTurns)
	candidates := make([]store.CheckpointCandidate, 0, coverageEnd)
	for end := coverageEnd; end >= 1; end-- {
		candidates = append(candidates, store.CheckpointCandidate{
			PrefixHash:       PrefixHash(req, end),
			CoverageEndIndex: end,
		})
	}

	coverageHash := ""
	if coverageEnd > 0 {
		coverageHash = PrefixHash(req, coverageEnd)
	}

	return Analysis{
		PrefixHash:      PrefixHash(req, len(req.Messages)),
		RequestHash:     RequestHash(req),
		MessageCount:    len(req.Messages),
		EstimatedTokens: EstimateTokens(req),
		CoverageEnd:     coverageEnd,
		CoverageHash:    coverageHash,
		Candidates:      candidates,
	}
}

func PrefixHash(req *anthropic.Request, messageEnd int) string {
	if req == nil {
		return ""
	}
	if messageEnd < 0 {
		messageEnd = 0
	}
	if messageEnd > len(req.Messages) {
		messageEnd = len(req.Messages)
	}

	messagesRaw, _ := json.Marshal(req.Messages[:messageEnd])
	prefix := map[string]json.RawMessage{
		"messages": messagesRaw,
	}

	for _, key := range []string{"tools", "system", "thinking", "tool_choice"} {
		if value, ok := req.RawField(key); ok {
			prefix[key] = value
		}
	}

	raw, _ := json.Marshal(prefix)
	return hash(raw)
}

func RequestHash(req *anthropic.Request) string {
	if req == nil {
		return ""
	}
	raw, err := json.Marshal(req.Raw)
	if err != nil {
		return ""
	}
	return hash(raw)
}

func EstimateTokens(req *anthropic.Request) int {
	if req == nil {
		return 0
	}
	total := 0
	for _, value := range req.Raw {
		total += len(value)
	}
	if total == 0 {
		total = len(req.Body)
	}
	return total/4 + 1
}

func EstimateBodyTokens(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	return len(body)/4 + 1
}

func RecentTurnStart(messages []anthropic.Message, keepRecentTurns int) int {
	if keepRecentTurns <= 0 {
		return len(messages)
	}

	seenUserTurns := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			seenUserTurns++
			if seenUserTurns == keepRecentTurns {
				return i
			}
		}
	}

	if seenUserTurns > 0 {
		return 0
	}
	if len(messages) <= keepRecentTurns {
		return 0
	}
	return len(messages) - keepRecentTurns
}

func hash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
