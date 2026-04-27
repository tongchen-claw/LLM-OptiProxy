package httpserver

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tongchen-claw/LLM-OptiProxy/internal/anthropic"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/audit"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/canonicalize"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/checkpoint"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/config"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/store"
)

type Dependencies struct {
	Config      config.Config
	Audit       *audit.Logger
	Store       store.Store
	Checkpoints *checkpoint.Manager
}

type Server struct {
	cfg         config.Config
	audit       *audit.Logger
	store       store.Store
	checkpoints *checkpoint.Manager
	client      *http.Client
	counter     atomic.Uint64
}

func New(deps Dependencies) http.Handler {
	client := &http.Client{}
	if deps.Config.UpstreamTimeout > 0 {
		client.Timeout = deps.Config.UpstreamTimeout
	}

	s := &Server{
		cfg:         deps.Config,
		audit:       deps.Audit,
		store:       deps.Store,
		checkpoints: deps.Checkpoints,
		client:      client,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/messages", s.handleMessages)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	requestID := s.nextRequestID()
	start := time.Now()

	record := audit.Record{
		RequestID:                 requestID,
		CheckpointCompressor:      s.cfg.CheckpointCompressor,
		CheckpointCompressorModel: s.cfg.CompressorModel,
		CreatedAt:                 time.Now().UTC(),
	}

	if r.Method != http.MethodPost {
		record.ErrorClass = "method_not_allowed"
		record.DurationMs = time.Since(start).Milliseconds()
		s.audit.Log(record)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := readRequestBody(w, r, s.cfg.MaxBodyBytes)
	if err != nil {
		record.ErrorClass = "request_body_read_failed"
		record.DurationMs = time.Since(start).Milliseconds()
		s.audit.Log(record)
		return
	}
	record.OriginalEstimatedInputTokens = canonicalize.EstimateBodyTokens(body)
	record.CustomerOriginalInputTokens = record.OriginalEstimatedInputTokens

	forwardBody := body
	passthroughReason := ""
	var parsed *anthropic.Request
	var analysis canonicalize.Analysis

	if req, err := anthropic.ParseRequest(body); err == nil {
		parsed = req
		analysis = canonicalize.Analyze(req, s.cfg.KeepRecentTurns)
		record.PrefixHash = analysis.PrefixHash
		record.Model = req.Model
		record.Stream = req.Stream
		record.OriginalEstimatedInputTokens = analysis.EstimatedTokens
		record.CustomerOriginalInputTokens = analysis.EstimatedTokens

		s.store.UpsertPrefix(store.PrefixNode{
			PrefixHash:      analysis.PrefixHash,
			RequestHash:     analysis.RequestHash,
			MessageCount:    analysis.MessageCount,
			EstimatedTokens: analysis.EstimatedTokens,
		})

		apply := s.checkpoints.Apply(req, analysis)
		passthroughReason = apply.PassthroughReason
		if apply.CheckpointHit {
			forwardBody = apply.Body
			record.CheckpointHit = true
			record.CheckpointCoverageEndIndex = apply.CoverageEndIndex
			record.CheckpointCompressor = apply.CompressorMode
			record.CheckpointCompressorModel = apply.CompressorModel
			passthroughReason = ""
		}
	} else {
		passthroughReason = "request_parse_failed"
	}
	record.ForwardEstimatedInputTokens = estimateForwardTokens(forwardBody, record.OriginalEstimatedInputTokens)
	if record.CheckpointHit {
		record.EstimatedSavedInputTokens = maxInt(0, record.OriginalEstimatedInputTokens-record.ForwardEstimatedInputTokens)
		record.SavedInputTokens = record.EstimatedSavedInputTokens
		record.SavedInputTokenPercent = savedPercent(record.SavedInputTokens, record.CustomerOriginalInputTokens)
	}

	resp, err := s.forward(r.Context(), r, forwardBody)
	if err != nil {
		record.ErrorClass = "upstream_request_failed"
		record.PassthroughReason = passthroughReason
		record.DurationMs = time.Since(start).Milliseconds()
		s.audit.Log(record)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	record.UpstreamStatus = resp.StatusCode

	var usage audit.Usage
	isStream := (parsed != nil && parsed.Stream) || strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
	if isStream {
		usage, err = relaySSE(w, resp)
	} else {
		usage, err = relayResponse(w, resp)
	}

	record.DurationMs = time.Since(start).Milliseconds()
	record.InputTokens = usage.InputTokens
	record.OutputTokens = usage.OutputTokens
	record.CacheCreationInputTokens = usage.CacheCreationInputTokens
	record.CacheReadInputTokens = usage.CacheReadInputTokens
	record.PassthroughReason = passthroughReason
	if err != nil {
		record.ErrorClass = "response_relay_failed"
	}
	s.audit.Log(record)

	if err == nil && parsed != nil && resp.StatusCode >= 200 && resp.StatusCode < 500 {
		s.checkpoints.Schedule(parsed, analysis)
	}
}

func (s *Server) forward(ctx context.Context, original *http.Request, body []byte) (*http.Response, error) {
	target, err := buildUpstreamURL(s.cfg.UpstreamBaseURL, original.URL)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, original.Method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(body))

	copyRequestHeaders(req.Header, original.Header)
	return s.client.Do(req)
}

func estimateForwardTokens(body []byte, fallback int) int {
	req, err := anthropic.ParseRequest(body)
	if err != nil {
		return canonicalize.EstimateBodyTokens(body)
	}
	estimated := canonicalize.EstimateTokens(req)
	if estimated == 0 {
		return fallback
	}
	return estimated
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func savedPercent(savedTokens int, originalTokens int) float64 {
	if savedTokens <= 0 || originalTokens <= 0 {
		return 0
	}
	return float64(savedTokens) * 100 / float64(originalTokens)
}

func readRequestBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, error) {
	if maxBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		status := http.StatusBadRequest
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, http.StatusText(status), status)
		return nil, err
	}
	return body, nil
}

func relayResponse(w http.ResponseWriter, resp *http.Response) (audit.Usage, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return audit.Usage{}, err
	}

	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, writeErr := w.Write(body)
	return audit.ExtractUsage(body), writeErr
}

func relaySSE(w http.ResponseWriter, resp *http.Response) (audit.Usage, error) {
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	reader := bufio.NewReader(resp.Body)

	var usage audit.Usage
	var eventName string
	var data strings.Builder

	processEvent := func() {
		if data.Len() == 0 {
			eventName = ""
			return
		}
		if eventName == "message_delta" || eventName == "message_start" || eventName == "" {
			usage = audit.MergeUsage(usage, audit.ExtractUsage([]byte(data.String())))
		}
		eventName = ""
		data.Reset()
	}

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if _, writeErr := w.Write(line); writeErr != nil {
				return usage, writeErr
			}
			if flusher != nil {
				flusher.Flush()
			}

			trimmed := strings.TrimRight(string(line), "\r\n")
			switch {
			case trimmed == "":
				processEvent()
			case strings.HasPrefix(trimmed, "event:"):
				eventName = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
			case strings.HasPrefix(trimmed, "data:"):
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
			}
		}
		if err == io.EOF {
			processEvent()
			return usage, nil
		}
		if err != nil {
			return usage, err
		}
	}
}

func buildUpstreamURL(base string, original *url.URL) (string, error) {
	if base == "" {
		return "", fmt.Errorf("empty upstream base URL")
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	parsed.Path = singleJoiningSlash(parsed.Path, original.Path)
	parsed.RawQuery = original.RawQuery
	return parsed.String(), nil
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	default:
		return a + b
	}
}

func copyRequestHeaders(dst, src http.Header) {
	for key, values := range src {
		if shouldSkipHeader(key) ||
			strings.EqualFold(key, "Host") ||
			strings.EqualFold(key, "Content-Length") ||
			strings.EqualFold(key, "Accept-Encoding") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		if shouldSkipHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func shouldSkipHeader(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func (s *Server) nextRequestID() string {
	value := s.counter.Add(1)
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatUint(value, 36)
}
