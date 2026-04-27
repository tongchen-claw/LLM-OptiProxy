package store

import (
	"fmt"
	"sync"
	"time"
)

type PrefixNode struct {
	PrefixHash       string
	ParentPrefixHash string
	RequestHash      string
	MessageCount     int
	EstimatedTokens  int
	CreatedAt        time.Time
	LastSeenAt       time.Time
}

type CheckpointCandidate struct {
	PrefixHash       string
	CoverageEndIndex int
}

type CheckpointRecord struct {
	PrefixHash       string
	ParentPrefixHash string
	CoverageEndIndex int
	KeepRecentTurns  int
	SummaryFormat    string
	SummaryText      string
	CompressorMode   string
	CompressorModel  string
	BuildStatus      string
	ErrorClass       string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Store interface {
	UpsertPrefix(node PrefixNode)
	FindReadyCheckpoint(candidates []CheckpointCandidate, keepRecentTurns int, compressorMode string, compressorModel string) (CheckpointRecord, bool)
	HasBuildingCheckpoint(candidates []CheckpointCandidate, keepRecentTurns int, compressorMode string, compressorModel string) bool
	TryStartCheckpoint(record CheckpointRecord) bool
	CompleteCheckpoint(record CheckpointRecord)
	FailCheckpoint(record CheckpointRecord, errorClass string)
}

type MemoryStore struct {
	mu          sync.Mutex
	prefixes    map[string]PrefixNode
	checkpoints map[string]CheckpointRecord
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		prefixes:    make(map[string]PrefixNode),
		checkpoints: make(map[string]CheckpointRecord),
	}
}

func (s *MemoryStore) UpsertPrefix(node PrefixNode) {
	if node.PrefixHash == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	existing, ok := s.prefixes[node.PrefixHash]
	if ok {
		node.CreatedAt = existing.CreatedAt
	} else if node.CreatedAt.IsZero() {
		node.CreatedAt = now
	}
	node.LastSeenAt = now
	s.prefixes[node.PrefixHash] = node
}

func (s *MemoryStore) FindReadyCheckpoint(candidates []CheckpointCandidate, keepRecentTurns int, compressorMode string, compressorModel string) (CheckpointRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, candidate := range candidates {
		key := checkpointKey(candidate.PrefixHash, candidate.CoverageEndIndex, keepRecentTurns, compressorMode, compressorModel)
		record, ok := s.checkpoints[key]
		if ok && record.BuildStatus == "ready" {
			return record, true
		}
	}
	return CheckpointRecord{}, false
}

func (s *MemoryStore) HasBuildingCheckpoint(candidates []CheckpointCandidate, keepRecentTurns int, compressorMode string, compressorModel string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, candidate := range candidates {
		key := checkpointKey(candidate.PrefixHash, candidate.CoverageEndIndex, keepRecentTurns, compressorMode, compressorModel)
		record, ok := s.checkpoints[key]
		if ok && record.BuildStatus == "building" {
			return true
		}
	}
	return false
}

func (s *MemoryStore) TryStartCheckpoint(record CheckpointRecord) bool {
	if record.PrefixHash == "" || record.CoverageEndIndex <= 0 {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := checkpointKey(record.PrefixHash, record.CoverageEndIndex, record.KeepRecentTurns, record.CompressorMode, record.CompressorModel)
	existing, ok := s.checkpoints[key]
	if ok && (existing.BuildStatus == "building" || existing.BuildStatus == "ready") {
		return false
	}

	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	record.BuildStatus = "building"
	s.checkpoints[key] = record
	return true
}

func (s *MemoryStore) CompleteCheckpoint(record CheckpointRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := checkpointKey(record.PrefixHash, record.CoverageEndIndex, record.KeepRecentTurns, record.CompressorMode, record.CompressorModel)
	existing := s.checkpoints[key]
	if !existing.CreatedAt.IsZero() {
		record.CreatedAt = existing.CreatedAt
	}
	record.BuildStatus = "ready"
	record.ErrorClass = ""
	record.UpdatedAt = time.Now().UTC()
	s.checkpoints[key] = record
}

func (s *MemoryStore) FailCheckpoint(record CheckpointRecord, errorClass string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := checkpointKey(record.PrefixHash, record.CoverageEndIndex, record.KeepRecentTurns, record.CompressorMode, record.CompressorModel)
	existing := s.checkpoints[key]
	if !existing.CreatedAt.IsZero() {
		record.CreatedAt = existing.CreatedAt
	}
	record.BuildStatus = "failed"
	record.ErrorClass = errorClass
	record.UpdatedAt = time.Now().UTC()
	s.checkpoints[key] = record
}

func checkpointKey(prefixHash string, coverageEndIndex, keepRecentTurns int, compressorMode string, compressorModel string) string {
	return fmt.Sprintf("%s:%d:%d:%s:%s", prefixHash, coverageEndIndex, keepRecentTurns, compressorMode, compressorModel)
}
