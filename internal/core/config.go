package core

import (
	"errors"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Channel creation policies (Decision 93).
const (
	ChannelCreationOpen   = "open"
	ChannelCreationAdmins = "admins"
)

// Config is the core's configuration. The zero value is not valid; start
// from Defaults. The v1 fields that concern the store keep their v1 names,
// defaults and bounds (v1 internal/config/config.go).
type Config struct {
	// SessionLeaseSeconds is how long Open, Receive and Watch extend a
	// session's lease (Decisions 42, 79).
	SessionLeaseSeconds int
	// LeaseSkewSeconds is the grace added to a lease before a session counts
	// as lapsed (Decision 49).
	LeaseSkewSeconds int
	// SessionRetentionHours is how long a lapsed session row is kept for
	// resume (Decision 79).
	SessionRetentionHours int
	// WatchMaxSeconds caps a Watch timeout (Decision 42).
	WatchMaxSeconds int
	// WatchMergeSeconds is the window in which a wake merges further messages
	// (Decision 45).
	WatchMergeSeconds int
	// MaintenanceIntervalSeconds is the maintenance tick period.
	MaintenanceIntervalSeconds int
	// ChannelCreation is "open" (any registered identity) or "admins"
	// (Decision 93). A string so the zero value cannot silently mean closed.
	ChannelCreation string
	// DefaultACL builds a new channel's ACL when the creator gives none; nil
	// means store.DefaultACL (Decision 88).
	DefaultACL func(creator string) []store.ACLEntry

	MessageRetentionHours int
	TombstoneMinHours     int
	TaskExpiryHours       int // 0 disables the rule (v1 ADR 0013)
	MemoryExpiryHours     int // 0 disables the rule
	// EmptyChannelReapHours is the age after which an ordinary channel with
	// no messages and no subscriptions is deleted (Decision (Eric,
	// 2026-10-21), Milestone 3 item 8; key empty_channel_reap_hours).
	EmptyChannelReapHours   int
	ReceiptRetentionMinutes int
	MaxMessageKiB           int
	SendMessagesPerSecond   int
	SendKiBPerSecond        int
	ReceiveDefaultCount     int
	ReceiveMaxCount         int
	ResultDefaultKiB        int

	// EmbeddingModel names the model of the Embedder; it must equal
	// Embedder.Model() when one is given (checked in New).
	EmbeddingModel               string
	EmbeddingQueryTimeoutSeconds float64
}

// Defaults returns the configuration with every default of plan choice 6.
func Defaults() Config {
	return Config{
		SessionLeaseSeconds:          90,
		LeaseSkewSeconds:             5,
		SessionRetentionHours:        72,
		WatchMaxSeconds:              50,
		WatchMergeSeconds:            2,
		MaintenanceIntervalSeconds:   60,
		ChannelCreation:              ChannelCreationOpen,
		MessageRetentionHours:        168,
		TombstoneMinHours:            72,
		TaskExpiryHours:              720,
		MemoryExpiryHours:            8760,
		EmptyChannelReapHours:        24,
		ReceiptRetentionMinutes:      60,
		MaxMessageKiB:                64,
		SendMessagesPerSecond:        100,
		SendKiBPerSecond:             1024,
		ReceiveDefaultCount:          100,
		ReceiveMaxCount:              1000,
		ResultDefaultKiB:             64,
		EmbeddingQueryTimeoutSeconds: 10,
	}
}

// Validate applies the v1 bounds and the v2 additions; the error names the
// offending setting.
func (c Config) Validate() error {
	bounds := []struct {
		name     string
		v        int
		min, max int
	}{
		{"session_lease_seconds", c.SessionLeaseSeconds, 1, 86400},
		{"lease_skew_seconds", c.LeaseSkewSeconds, 0, 300},
		{"session_retention_hours", c.SessionRetentionHours, 1, 8760},
		{"watch_max_seconds", c.WatchMaxSeconds, 1, 240},
		{"watch_merge_seconds", c.WatchMergeSeconds, 0, 60},
		{"maintenance_interval_seconds", c.MaintenanceIntervalSeconds, 1, 3600},
		{"message_retention_hours", c.MessageRetentionHours, 1, 8760},
		{"tombstone_min_hours", c.TombstoneMinHours, 72, 720},
		{"receipt_retention_minutes", c.ReceiptRetentionMinutes, 1, 1440},
		{"max_message_kib", c.MaxMessageKiB, 1, 1024},
		{"send_messages_per_second", c.SendMessagesPerSecond, 1, 10000},
		{"send_kib_per_second", c.SendKiBPerSecond, 64, 65536},
		{"receive_default_count", c.ReceiveDefaultCount, 1, 1000},
		{"receive_max_count", c.ReceiveMaxCount, 1, 10000},
		{"result_default_kib", c.ResultDefaultKiB, 1, 4096},
		{"empty_channel_reap_hours", c.EmptyChannelReapHours, 1, 8760},
		{"task_expiry_hours", c.TaskExpiryHours, 0, 87600},
		{"memory_expiry_hours", c.MemoryExpiryHours, 0, 87600},
	}
	for _, b := range bounds {
		if b.v < b.min || b.v > b.max {
			return fmt.Errorf("%s must be between %d and %d, got %d", b.name, b.min, b.max, b.v)
		}
	}
	if c.ReceiveDefaultCount > c.ReceiveMaxCount {
		return errors.New("receive_default_count must be <= receive_max_count")
	}
	if c.EmbeddingQueryTimeoutSeconds < 0.1 || c.EmbeddingQueryTimeoutSeconds > 120 {
		return fmt.Errorf("embedding_query_timeout_seconds must be between 0.1 and 120, got %v", c.EmbeddingQueryTimeoutSeconds)
	}
	switch c.ChannelCreation {
	case ChannelCreationOpen, ChannelCreationAdmins:
	default:
		return fmt.Errorf("channel_creation must be %q or %q, got %q", ChannelCreationOpen, ChannelCreationAdmins, c.ChannelCreation)
	}
	return nil
}
