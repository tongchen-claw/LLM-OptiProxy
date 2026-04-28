package config

import "testing"

func TestLoadSavingsLogPath(t *testing.T) {
	t.Setenv("OPTIPROXY_SAVINGS_LOG_PATH", "data/savings.jsonl")

	cfg := Load()

	if cfg.SavingsLogPath != "data/savings.jsonl" {
		t.Fatalf("SavingsLogPath = %q, want data/savings.jsonl", cfg.SavingsLogPath)
	}
}
