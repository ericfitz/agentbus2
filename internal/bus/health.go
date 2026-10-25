package bus

import "context"

// HealthStatus is the overall verdict of the health document.
type HealthStatus string

// The health statuses; an unreachable store is an error, never a status.
const (
	HealthOK       HealthStatus = "ok"
	HealthDegraded HealthStatus = "degraded"
)

// ValidHealthStatus reports whether s is one of the statuses above.
func ValidHealthStatus(s HealthStatus) bool { return s == HealthOK || s == HealthDegraded }

// HealthTier is how much of the document the caller may see (Decision 73).
type HealthTier string

// The tiers: an unauthenticated probe, a registered identity, an administrator.
const (
	TierProbe    HealthTier = "probe"
	TierIdentity HealthTier = "identity"
	TierAdmin    HealthTier = "admin"
)

// ValidHealthTier reports whether t is one of the tiers above.
func ValidHealthTier(t HealthTier) bool {
	return t == TierProbe || t == TierIdentity || t == TierAdmin
}

// EmbeddingState is the state of semantic search.
type EmbeddingState string

// The embedding states: no embedder, working, or the embedding notice is set.
const (
	EmbeddingsOff     EmbeddingState = "off"
	EmbeddingsOK      EmbeddingState = "ok"
	EmbeddingsFailing EmbeddingState = "failing"
)

// ValidEmbeddingState reports whether s is one of the states above.
func ValidEmbeddingState(s EmbeddingState) bool {
	return s == EmbeddingsOff || s == EmbeddingsOK || s == EmbeddingsFailing
}

// MaintenanceLock is what this process's last maintenance tick found.
type MaintenanceLock string

// The lock states: this process holds it, another does, or no tick has
// answered yet (or the last one failed).
const (
	LockHeld          MaintenanceLock = "held"
	LockHeldElsewhere MaintenanceLock = "held_elsewhere"
	LockUnknown       MaintenanceLock = "unknown"
)

// ValidMaintenanceLock reports whether l is one of the lock states above.
func ValidMaintenanceLock(l MaintenanceLock) bool {
	return l == LockHeld || l == LockHeldElsewhere || l == LockUnknown
}

// Health is the server's health as the caller's tier may see it (spec
// 2026-10-25 section 4). Members above the probe tier are the zero value (nil
// pointers and slices) below their tier.
type Health struct {
	Status      HealthStatus
	Tier        HealthTier
	Version     string
	Now         int64
	StartedAt   int64              // identity tier and up; 0 below
	Store       *HealthStore       // nil below the identity tier
	Embeddings  *HealthEmbeddings  // nil below the identity tier
	Storage     *Usage             // nil below the admin tier
	Notices     []Notice           // nil below the admin tier; non-nil (possibly empty) at it
	Maintenance *HealthMaintenance // nil below the admin tier
}

// HealthStore names the engine and its schema version.
type HealthStore struct {
	Engine        string
	SchemaVersion int
}

// HealthEmbeddings is the embedding state and backlog. Model, LastError,
// LastErrorAt and RejectedLast are set at the admin tier only.
type HealthEmbeddings struct {
	State             EmbeddingState
	Backlog, Rejected int
	Model, LastError  string
	LastErrorAt       int64
	RejectedLast      string
}

// HealthMaintenance is this process's view of the maintenance loop (spec
// 2026-10-25 section 4, agent decision 6).
type HealthMaintenance struct {
	IntervalSeconds int
	Owner           string
	Lock            MaintenanceLock
	LastTickAt      int64
	LockError       string
	Jobs            []HealthJob
}

// HealthJob is one job's outcome in the last tick.
type HealthJob struct {
	Name    string
	Changed int
	Error   string
	Skipped bool
}

// Monitoring is the health seam: every caller may ask, the answer is tiered.
type Monitoring interface {
	Health(ctx context.Context) (Health, error)
}
