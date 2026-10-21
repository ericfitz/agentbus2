package core

import (
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
}

func TestConfigRejectsOutOfRange(t *testing.T) {
	cases := map[string]func(*Config){
		"session_lease_seconds":        func(c *Config) { c.SessionLeaseSeconds = 0 },
		"lease_skew_seconds":           func(c *Config) { c.LeaseSkewSeconds = -1 },
		"session_retention_hours":      func(c *Config) { c.SessionRetentionHours = 0 },
		"watch_max_seconds":            func(c *Config) { c.WatchMaxSeconds = 0 },
		"watch_merge_seconds":          func(c *Config) { c.WatchMergeSeconds = 61 },
		"maintenance_interval_seconds": func(c *Config) { c.MaintenanceIntervalSeconds = 0 },
		"message_retention_hours":      func(c *Config) { c.MessageRetentionHours = 8761 },
		"tombstone_min_hours":          func(c *Config) { c.TombstoneMinHours = 71 },
		"receipt_retention_minutes":    func(c *Config) { c.ReceiptRetentionMinutes = 1441 },
		"max_message_kib":              func(c *Config) { c.MaxMessageKiB = 0 },
		"send_messages_per_second":     func(c *Config) { c.SendMessagesPerSecond = 10001 },
		"send_kib_per_second":          func(c *Config) { c.SendKiBPerSecond = 63 },
		"receive_default_count":        func(c *Config) { c.ReceiveDefaultCount = 0 },
		"receive_max_count":            func(c *Config) { c.ReceiveMaxCount = 10001 },
		"result_default_kib":           func(c *Config) { c.ResultDefaultKiB = 4097 },
		"task_expiry_hours":            func(c *Config) { c.TaskExpiryHours = -1 },
		"memory_expiry_hours":          func(c *Config) { c.MemoryExpiryHours = 87601 },
		"embedding_query_timeout":      func(c *Config) { c.EmbeddingQueryTimeoutSeconds = 0.01 },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			c := Defaults()
			mod(&c)
			if err := c.Validate(); err == nil {
				t.Fatalf("Validate accepted %s out of range", name)
			}
		})
	}
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
