package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/tongchen-claw/LLM-OptiProxy/internal/audit"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/checkpoint"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/config"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/httpserver"
	"github.com/tongchen-claw/LLM-OptiProxy/internal/store"
)

func main() {
	cfg := config.Load()

	auditLogger, err := audit.NewLogger(cfg.AuditLogPath, cfg.UsageSQLitePath, cfg.EnableAudit)
	if err != nil {
		log.Fatalf("create audit logger: %v", err)
	}
	defer auditLogger.Close()

	state := store.NewMemoryStore()
	checkpoints := checkpoint.NewManager(cfg, state)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	checkpoints.Start(ctx)

	handler := httpserver.New(httpserver.Dependencies{
		Config:      cfg,
		Audit:       auditLogger,
		Store:       state,
		Checkpoints: checkpoints,
	})

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("http shutdown error: %v", err)
		}
	}()

	log.Printf("config: listen_addr=%s upstream_base_url=%s enable_audit=%t audit_log_path=%s usage_sqlite_path=%s", cfg.ListenAddr, cfg.UpstreamBaseURL, cfg.EnableAudit, cfg.AuditLogPath, cfg.UsageSQLitePath)
	log.Printf(
		"config: enable_checkpointing=%t checkpoint_compressor=%s compressor_base_url=%s compressor_model=%s compressor_api_key_configured=%t keep_recent_turns=%d token_soft_limit=%d message_count_threshold=%d checkpoint_async_workers=%d",
		cfg.EnableCheckpointing,
		cfg.CheckpointCompressor,
		cfg.CompressorBaseURL,
		cfg.CompressorModel,
		cfg.CompressorAPIKey != "",
		cfg.KeepRecentTurns,
		cfg.TokenSoftLimit,
		cfg.MessageCountThreshold,
		cfg.CheckpointAsyncWorkers,
	)
	log.Printf("listening on %s", cfg.ListenAddr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("http server: %v", err)
	}
}
