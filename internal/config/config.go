package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr              string
	UpstreamBaseURL         string
	EnableAudit             bool
	AssumeClientPromptCache bool
	EnableCheckpointing     bool
	CheckpointCompressor    string
	CompressorBaseURL       string
	CompressorModel         string
	CompressorAPIKey        string
	TokenSoftLimit          int
	MessageCountThreshold   int
	KeepRecentTurns         int
	CheckpointAsyncWorkers  int
	AuditLogPath            string
	UsageSQLitePath         string
	MaxBodyBytes            int64
	MaxSummaryChars         int
	UpstreamTimeout         time.Duration
}

func Load() Config {
	return Config{
		ListenAddr:              envString("OPTIPROXY_LISTEN_ADDR", ":8080"),
		UpstreamBaseURL:         strings.TrimRight(envString("OPTIPROXY_UPSTREAM_BASE_URL", "https://api.anthropic.com"), "/"),
		EnableAudit:             envBool("OPTIPROXY_ENABLE_AUDIT", true),
		AssumeClientPromptCache: envBool("OPTIPROXY_ASSUME_CLIENT_PROMPT_CACHE", true),
		EnableCheckpointing:     envBool("OPTIPROXY_ENABLE_CHECKPOINTING", false),
		CheckpointCompressor:    envString("OPTIPROXY_CHECKPOINT_COMPRESSOR", "local-extractive"),
		CompressorBaseURL:       strings.TrimRight(envString("OPTIPROXY_COMPRESSOR_BASE_URL", ""), "/"),
		CompressorModel:         envString("OPTIPROXY_COMPRESSOR_MODEL", ""),
		CompressorAPIKey:        envString("OPTIPROXY_COMPRESSOR_API_KEY", ""),
		TokenSoftLimit:          envInt("OPTIPROXY_TOKEN_SOFT_LIMIT", 32000),
		MessageCountThreshold:   envInt("OPTIPROXY_MESSAGE_COUNT_THRESHOLD", 10),
		KeepRecentTurns:         envInt("OPTIPROXY_KEEP_RECENT_TURNS", 3),
		CheckpointAsyncWorkers:  envInt("OPTIPROXY_CHECKPOINT_ASYNC_WORKERS", 1),
		AuditLogPath:            envString("OPTIPROXY_AUDIT_LOG_PATH", "data/audit.jsonl"),
		UsageSQLitePath:         envString("OPTIPROXY_USAGE_SQLITE_PATH", "data/usage.sqlite"),
		MaxBodyBytes:            int64(envInt("OPTIPROXY_MAX_BODY_BYTES", 32*1024*1024)),
		MaxSummaryChars:         envInt("OPTIPROXY_MAX_SUMMARY_CHARS", 6000),
		UpstreamTimeout:         time.Duration(envInt("OPTIPROXY_UPSTREAM_TIMEOUT_SECONDS", 0)) * time.Second,
	}
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	switch strings.ToLower(value) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
