package core

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestDefaultsValidate(t *testing.T) {
	if err := Defaults().Validate(); err != nil {
		t.Fatalf("Defaults().Validate() = %v", err)
	}
	d := Defaults()
	if d.SessionLeaseSeconds != 90 || d.LeaseSkewSeconds != 5 || d.ChannelCreation != "open" || d.WatchMaxSeconds != 50 {
		t.Errorf("defaults = %+v", d)
	}
	if d.InspectionTimeoutSeconds != 5 || d.InspectionOnFailure != "closed" {
		t.Errorf("inspection defaults = %v, %q, want 5, \"closed\"", d.InspectionTimeoutSeconds, d.InspectionOnFailure)
	}
	if d.ScopeIdleHours != 336 {
		t.Errorf("ScopeIdleHours default = %d, want 336", d.ScopeIdleHours)
	}
}

// TestConfigBounds checks both sides of every bound against literals that do
// not come from Validate: min-1 and max+1 are refused with the setting's name,
// min and max are accepted.
func TestConfigBounds(t *testing.T) {
	cases := []struct {
		name     string
		min, max int
		set      func(*Config, int)
	}{
		{"session_lease_seconds", 1, 86400, func(c *Config, v int) { c.SessionLeaseSeconds = v }},
		{"lease_skew_seconds", 0, 300, func(c *Config, v int) { c.LeaseSkewSeconds = v }},
		{"session_retention_hours", 1, 8760, func(c *Config, v int) { c.SessionRetentionHours = v }},
		{"watch_max_seconds", 1, 240, func(c *Config, v int) { c.WatchMaxSeconds = v }},
		{"watch_merge_seconds", 0, 60, func(c *Config, v int) { c.WatchMergeSeconds = v }},
		{"maintenance_interval_seconds", 1, 3600, func(c *Config, v int) { c.MaintenanceIntervalSeconds = v }},
		{"message_retention_hours", 1, 8760, func(c *Config, v int) { c.MessageRetentionHours = v }},
		{"tombstone_min_hours", 72, 720, func(c *Config, v int) { c.TombstoneMinHours = v }},
		{"receipt_retention_minutes", 1, 1440, func(c *Config, v int) { c.ReceiptRetentionMinutes = v }},
		{"max_message_kib", 1, 1024, func(c *Config, v int) { c.MaxMessageKiB = v }},
		{"send_messages_per_second", 1, 10000, func(c *Config, v int) { c.SendMessagesPerSecond = v }},
		{"send_kib_per_second", 64, 65536, func(c *Config, v int) { c.SendKiBPerSecond = v }},
		// The default count must not exceed the max count, so each of the two
		// is moved clear of the other when it is set at its own bound.
		{"receive_default_count", 1, 1000, func(c *Config, v int) { c.ReceiveDefaultCount, c.ReceiveMaxCount = v, 10000 }},
		{"receive_max_count", 1, 10000, func(c *Config, v int) { c.ReceiveDefaultCount, c.ReceiveMaxCount = 1, v }},
		{"result_default_kib", 1, 4096, func(c *Config, v int) { c.ResultDefaultKiB = v }},
		{"sqlite_budget_mib", 64, 1048576, func(c *Config, v int) { c.SQLiteBudgetMiB = v }},
		{"postgres_budget_mib", 64, 1048576, func(c *Config, v int) { c.PostgresBudgetMiB = v }},
		{"idle_window_hours", TombstoneFloorHours, 8760, func(c *Config, v int) { c.IdleWindowHours = v }},
		{"cleanup_free_percent", 5, 90, func(c *Config, v int) { c.CleanupFreePercent = v }},
		{"empty_channel_reap_hours", 1, 8760, func(c *Config, v int) { c.EmptyChannelReapHours = v }},
		{"scope_idle_hours", 1, 8760, func(c *Config, v int) { c.ScopeIdleHours = v }},
		{"task_expiry_hours", 0, 87600, func(c *Config, v int) { c.TaskExpiryHours = v }},
		{"memory_expiry_hours", 0, 87600, func(c *Config, v int) { c.MemoryExpiryHours = v }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, ok := range []int{tc.min, tc.max} {
				c := Defaults()
				tc.set(&c, ok)
				if err := c.Validate(); err != nil {
					t.Errorf("%s = %d rejected: %v", tc.name, ok, err)
				}
			}
			for _, bad := range []int{tc.min - 1, tc.max + 1} {
				c := Defaults()
				tc.set(&c, bad)
				err := c.Validate()
				if err == nil || !strings.Contains(err.Error(), tc.name) {
					t.Errorf("%s = %d: err = %v, want one naming the setting", tc.name, bad, err)
				}
			}
		})
	}
	t.Run("embedding_query_timeout_seconds", func(t *testing.T) {
		for _, ok := range []float64{0.1, 120} {
			c := Defaults()
			c.EmbeddingQueryTimeoutSeconds = ok
			if err := c.Validate(); err != nil {
				t.Errorf("embedding_query_timeout_seconds = %v rejected: %v", ok, err)
			}
		}
		for _, bad := range []float64{0.09, 120.1, 0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
			c := Defaults()
			c.EmbeddingQueryTimeoutSeconds = bad
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "embedding_query_timeout_seconds") {
				t.Errorf("embedding_query_timeout_seconds = %v: err = %v, want one naming the setting", bad, err)
			}
		}
	})
	t.Run("inspection_timeout_seconds", func(t *testing.T) {
		for _, ok := range []float64{0.1, 60} {
			c := Defaults()
			c.InspectionTimeoutSeconds = ok
			if err := c.Validate(); err != nil {
				t.Errorf("inspection_timeout_seconds = %v rejected: %v", ok, err)
			}
		}
		for _, bad := range []float64{0.09, 60.1, 0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
			c := Defaults()
			c.InspectionTimeoutSeconds = bad
			want := fmt.Sprintf("inspection_timeout_seconds must be between 0.1 and 60, got %v", bad)
			if err := c.Validate(); err == nil || err.Error() != want {
				t.Errorf("inspection_timeout_seconds = %v: err = %v, want %q", bad, err, want)
			}
		}
	})
	t.Run("inspection_on_failure", func(t *testing.T) {
		for _, ok := range []string{"closed", "open"} {
			c := Defaults()
			c.InspectionOnFailure = ok
			if err := c.Validate(); err != nil {
				t.Errorf("inspection_on_failure = %q rejected: %v", ok, err)
			}
		}
		for _, bad := range []string{"maybe", "", "Open"} {
			c := Defaults()
			c.InspectionOnFailure = bad
			want := fmt.Sprintf("inspection_on_failure must be \"closed\" or \"open\", got %q", bad)
			if err := c.Validate(); err == nil || err.Error() != want {
				t.Errorf("inspection_on_failure = %q: err = %v, want %q", bad, err, want)
			}
		}
	})
	t.Run("default above max count", func(t *testing.T) {
		c := Defaults()
		c.ReceiveDefaultCount, c.ReceiveMaxCount = 50, 10
		if err := c.Validate(); err == nil {
			t.Fatal("receive_default_count > receive_max_count accepted")
		}
	})
	t.Run("channel_creation", func(t *testing.T) {
		for _, v := range []string{"closed", ""} {
			c := Defaults()
			c.ChannelCreation = v
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), `"open"`) || !strings.Contains(err.Error(), `"admins"`) {
				t.Errorf("ChannelCreation %q: err = %v, want one naming open and admins", v, err)
			}
		}
		c := Defaults()
		c.ChannelCreation = "admins"
		if err := c.Validate(); err != nil {
			t.Errorf("admins rejected: %v", err)
		}
	})
}

func TestNewChecksEmbedder(t *testing.T) {
	c, _ := newTestCore(t, nil)
	emb := fakeModelEmbedder("m1")
	if _, err := New(c.Store, Defaults(), Options{Embedder: emb}); err == nil {
		t.Error("an embedder with no EmbeddingModel accepted")
	}
	cfg := Defaults()
	cfg.EmbeddingModel = "other"
	if _, err := New(c.Store, cfg, Options{Embedder: emb}); err == nil {
		t.Error("an EmbeddingModel that differs from the embedder's accepted")
	}
	cfg.EmbeddingModel = "m1"
	got, err := New(c.Store, cfg, Options{Embedder: emb})
	if err != nil || got.Embedder() == nil || got.Owner == "" {
		t.Errorf("New with matching model = %v, %v", got, err)
	}
	if _, err := New(nil, Defaults(), Options{}); err == nil {
		t.Error("nil store accepted")
	}
}

// TestFloorsAreTheLowerBounds pins the eviction floors as the lower bounds of
// the two retention settings.
func TestFloorsAreTheLowerBounds(t *testing.T) {
	c := Defaults()
	c.MessageRetentionHours, c.TombstoneMinHours = MessageFloorHours, TombstoneFloorHours
	if err := c.Validate(); err != nil {
		t.Fatalf("floors rejected: %v", err)
	}
	c = Defaults()
	c.MessageRetentionHours = MessageFloorHours - 1
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "message_retention_hours") {
		t.Errorf("below the message floor: %v", err)
	}
	c = Defaults()
	c.TombstoneMinHours = TombstoneFloorHours - 1
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "tombstone_min_hours") {
		t.Errorf("below the tombstone floor: %v", err)
	}
}
