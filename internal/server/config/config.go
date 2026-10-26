package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Issuer is one external token issuer (Decision 57, spec 5.1): a corporate
// identity provider whose tokens are exchanged for own bearers.
type Issuer struct {
	Name       string       `json:"name"` // the handle `users add -issuer` and the bindings use
	Issuer     string       `json:"issuer"`
	JWKSURI    string       `json:"jwks_uri"`
	Audience   string       `json:"audience"`
	Algorithms []string     `json:"algorithms"`
	Claims     IssuerClaims `json:"claims"`
	// HumanTokenLifetimeSeconds is the lifetime of the human bearer minted
	// for this issuer; 0 means 900.
	HumanTokenLifetimeSeconds int `json:"human_token_lifetime_seconds"`
}

// IssuerClaims names the provider claims that carry the email, the groups and
// the subject; "" means the default (email, groups, sub).
type IssuerClaims struct {
	Email   string `json:"email"`
	Groups  string `json:"groups"`
	Subject string `json:"subject"`
}

// Config is the server configuration (Decision 85, spec 6.5).
type Config struct {
	Listen       string `json:"listen"`
	Registration string `json:"registration"` // "", open, enrollment
	SQLite       string `json:"sqlite"`
	Postgres     string `json:"postgres"`

	Issuer               string   `json:"issuer"`
	TokenLifetimeSeconds int      `json:"token_lifetime_seconds"`
	JWKSRefreshSeconds   int      `json:"jwks_refresh_seconds"`
	Issuers              []Issuer `json:"issuers"`

	DefaultACL      []ACLEntry `json:"default_acl"`
	ChannelCreation string     `json:"channel_creation"`

	ReceiptRetentionMinutes    int `json:"receipt_retention_minutes"`
	SessionRetentionHours      int `json:"session_retention_hours"`
	SendMessagesPerSecond      int `json:"send_messages_per_second"`
	SendKiBPerSecond           int `json:"send_kib_per_second"`
	MessageRetentionHours      int `json:"message_retention_hours"`
	TombstoneMinHours          int `json:"tombstone_min_hours"`
	TaskExpiryHours            int `json:"task_expiry_hours"`
	MemoryExpiryHours          int `json:"memory_expiry_hours"`
	EmptyChannelReapHours      int `json:"empty_channel_reap_hours"`
	ScopeIdleHours             int `json:"scope_idle_hours"`
	SessionLeaseSeconds        int `json:"session_lease_seconds"`
	LeaseSkewSeconds           int `json:"lease_skew_seconds"`
	WatchMaxSeconds            int `json:"watch_max_seconds"`
	WatchMergeSeconds          int `json:"watch_merge_seconds"`
	MaintenanceIntervalSeconds int `json:"maintenance_interval_seconds"`
	MaxMessageKiB              int `json:"max_message_kib"`
	ReceiveDefaultCount        int `json:"receive_default_count"`
	ReceiveMaxCount            int `json:"receive_max_count"`
	ResultDefaultKiB           int `json:"result_default_kib"`
	SQLiteBudgetMiB            int `json:"sqlite_budget_mib"`
	CleanupFreePercent         int `json:"cleanup_free_percent"`

	EmbeddingEndpoint            string  `json:"embedding_endpoint"`
	EmbeddingModel               string  `json:"embedding_model"`
	EmbeddingAPIKeyFile          string  `json:"embedding_api_key_file"`
	EmbeddingAPIKeyEnv           string  `json:"embedding_api_key_env"`
	EmbeddingQueryTimeoutSeconds float64 `json:"embedding_query_timeout_seconds"`

	// The Slack bridge (Slack bridge spec 5.1). Both token files or neither.
	SlackAppTokenFile          string `json:"slack_app_token_file"`
	SlackBotTokenFile          string `json:"slack_bot_token_file"`
	SlackArchiveAfterDoneHours int    `json:"slack_archive_after_done_hours"`
	SlackMaxMessageChars       int    `json:"slack_max_message_chars"`

	LogLevel string `json:"log_level"`
}

// Default returns the defaults; the core values come from core.Defaults.
func Default() Config {
	d := core.Defaults()
	return Config{
		TokenLifetimeSeconds: 3600,
		JWKSRefreshSeconds:   300,
		DefaultACL:           shippedDefault(),
		ChannelCreation:      d.ChannelCreation,
		LogLevel:             "info",

		SlackArchiveAfterDoneHours: 168,
		SlackMaxMessageChars:       3000,

		ReceiptRetentionMinutes:      d.ReceiptRetentionMinutes,
		SessionRetentionHours:        d.SessionRetentionHours,
		SendMessagesPerSecond:        d.SendMessagesPerSecond,
		SendKiBPerSecond:             d.SendKiBPerSecond,
		MessageRetentionHours:        d.MessageRetentionHours,
		TombstoneMinHours:            d.TombstoneMinHours,
		TaskExpiryHours:              d.TaskExpiryHours,
		MemoryExpiryHours:            d.MemoryExpiryHours,
		EmptyChannelReapHours:        d.EmptyChannelReapHours,
		ScopeIdleHours:               d.ScopeIdleHours,
		SessionLeaseSeconds:          d.SessionLeaseSeconds,
		LeaseSkewSeconds:             d.LeaseSkewSeconds,
		WatchMaxSeconds:              d.WatchMaxSeconds,
		WatchMergeSeconds:            d.WatchMergeSeconds,
		MaintenanceIntervalSeconds:   d.MaintenanceIntervalSeconds,
		MaxMessageKiB:                d.MaxMessageKiB,
		ReceiveDefaultCount:          d.ReceiveDefaultCount,
		ReceiveMaxCount:              d.ReceiveMaxCount,
		ResultDefaultKiB:             d.ResultDefaultKiB,
		SQLiteBudgetMiB:              d.SQLiteBudgetMiB,
		CleanupFreePercent:           d.CleanupFreePercent,
		EmbeddingQueryTimeoutSeconds: d.EmbeddingQueryTimeoutSeconds,
	}
}

// fields maps each json name to its struct field index, in struct order.
func fields() (names []string, index map[string]int) {
	t := reflect.TypeFor[Config]()
	index = make(map[string]int, t.NumField())
	for i := range t.NumField() {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		names = append(names, name)
		index[name] = i
	}
	return names, index
}

// Settings lists every json setting name in struct order.
func Settings() []string {
	names, _ := fields()
	return names
}

// Core maps the configuration onto core.Config. DefaultACL is nil when the
// list equals the shipped default, so store.DefaultACL applies unchanged.
func (c Config) Core() (core.Config, error) {
	if err := ValidateDefaultACL(c.DefaultACL); err != nil {
		return core.Config{}, err
	}
	cc := core.Config{
		SessionLeaseSeconds:          c.SessionLeaseSeconds,
		LeaseSkewSeconds:             c.LeaseSkewSeconds,
		SessionRetentionHours:        c.SessionRetentionHours,
		WatchMaxSeconds:              c.WatchMaxSeconds,
		WatchMergeSeconds:            c.WatchMergeSeconds,
		MaintenanceIntervalSeconds:   c.MaintenanceIntervalSeconds,
		ChannelCreation:              c.ChannelCreation,
		MessageRetentionHours:        c.MessageRetentionHours,
		TombstoneMinHours:            c.TombstoneMinHours,
		TaskExpiryHours:              c.TaskExpiryHours,
		MemoryExpiryHours:            c.MemoryExpiryHours,
		EmptyChannelReapHours:        c.EmptyChannelReapHours,
		ScopeIdleHours:               c.ScopeIdleHours,
		ReceiptRetentionMinutes:      c.ReceiptRetentionMinutes,
		MaxMessageKiB:                c.MaxMessageKiB,
		SendMessagesPerSecond:        c.SendMessagesPerSecond,
		SendKiBPerSecond:             c.SendKiBPerSecond,
		ReceiveDefaultCount:          c.ReceiveDefaultCount,
		ReceiveMaxCount:              c.ReceiveMaxCount,
		ResultDefaultKiB:             c.ResultDefaultKiB,
		SQLiteBudgetMiB:              c.SQLiteBudgetMiB,
		CleanupFreePercent:           c.CleanupFreePercent,
		EmbeddingModel:               c.EmbeddingModel,
		EmbeddingQueryTimeoutSeconds: c.EmbeddingQueryTimeoutSeconds,
	}
	if !IsShippedDefault(c.DefaultACL) {
		cc.DefaultACL = DefaultACLFunc(c.DefaultACL)
	}
	return cc, nil
}

// Validate checks the whole configuration. It never requires Listen.
func (c Config) Validate() error {
	if (c.SQLite == "") == (c.Postgres == "") {
		return errors.New("exactly one of sqlite or postgres is required")
	}
	switch c.Registration {
	case "", "open", "enrollment":
	default:
		return fmt.Errorf("registration must be open or enrollment, got %q", c.Registration)
	}
	if c.Issuer != "" {
		u, err := url.Parse(c.Issuer)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(c.Issuer, "?#") {
			return fmt.Errorf("issuer must be an absolute http or https URL without path, query, fragment or trailing slash, got %q", c.Issuer)
		}
	}
	if c.TokenLifetimeSeconds < 1 || c.TokenLifetimeSeconds > 86400 {
		return fmt.Errorf("token_lifetime_seconds must be between 1 and 86400, got %d", c.TokenLifetimeSeconds)
	}
	if c.JWKSRefreshSeconds < 10 || c.JWKSRefreshSeconds > 86400 {
		return fmt.Errorf("jwks_refresh_seconds must be between 10 and 86400, got %d", c.JWKSRefreshSeconds)
	}
	// The Verifier accepts only the own issuer's tokens, and the token
	// endpoint picks the entry by exact iss, so an entry equal to the own
	// issuer or to another entry would be ambiguous or unreachable.
	// An unset Issuer is the bound address, known only at Run.
	first := map[string]int{}
	names := map[string]int{}
	for i, is := range c.Issuers {
		if err := validateIssuer(is); err != nil {
			return fmt.Errorf("issuers: entry %d: %w", i, err)
		}
		if c.Issuer != "" && is.Issuer == c.Issuer {
			return fmt.Errorf("issuers: entry %d: issuer %q is the server's own issuer", i, is.Issuer)
		}
		if j, ok := first[is.Issuer]; ok {
			return fmt.Errorf("issuers: entry %d: issuer %q repeats entry %d", i, is.Issuer, j)
		}
		first[is.Issuer] = i
		if j, ok := names[is.Name]; ok {
			return fmt.Errorf("issuers: entry %d: name %q repeats entry %d", i, is.Name, j)
		}
		names[is.Name] = i
	}
	cc, err := c.Core()
	if err != nil {
		return err
	}
	if err := cc.Validate(); err != nil {
		return err
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log_level must be one of debug, info, warn, error, got %q", c.LogLevel)
	}
	if c.EmbeddingEndpoint != "" {
		if c.EmbeddingModel == "" {
			return errors.New("embedding_model is required when embedding_endpoint is set")
		}
		if !absoluteURL(c.EmbeddingEndpoint) {
			return fmt.Errorf("embedding_endpoint: must be an absolute URL with scheme and host, got %q", c.EmbeddingEndpoint)
		}
	}
	if c.EmbeddingAPIKeyFile != "" && c.EmbeddingAPIKeyEnv != "" {
		return errors.New("at most one of embedding_api_key_file and embedding_api_key_env may be set")
	}
	if (c.SlackAppTokenFile == "") != (c.SlackBotTokenFile == "") {
		return errors.New("slack_app_token_file and slack_bot_token_file must be set together")
	}
	if c.SlackArchiveAfterDoneHours < 1 || c.SlackArchiveAfterDoneHours > 8760 {
		return fmt.Errorf("slack_archive_after_done_hours must be between 1 and 8760, got %d", c.SlackArchiveAfterDoneHours)
	}
	if c.SlackMaxMessageChars < 200 || c.SlackMaxMessageChars > 3000 {
		return fmt.Errorf("slack_max_message_chars must be between 200 and 3000, got %d", c.SlackMaxMessageChars)
	}
	return nil
}

// Warnings lists what Validate accepts but an operator should see: an
// external issuer whose jwks_uri is plain http on a host other than loopback
// (an on-path attacker can substitute the key set). The spec states no
// transport rule for jwks_uri, so it is not refused.
func (c Config) Warnings() []string {
	var w []string
	for i, is := range c.Issuers {
		u, err := url.Parse(is.JWKSURI)
		if err != nil || !strings.EqualFold(u.Scheme, "http") || loopbackHost(u.Hostname()) {
			continue
		}
		w = append(w, fmt.Sprintf("issuers: entry %d: jwks_uri %q is plain http on a non-loopback host; anyone on the path can substitute the key set (use https)", i, is.JWKSURI))
	}
	return w
}

func loopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func absoluteURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme != "" && u.Host != ""
}

func validateIssuer(is Issuer) error {
	if is.Name == "" {
		return errors.New("name is required")
	}
	if err := store.NameRule(is.Name); err != nil {
		return fmt.Errorf("name %q %s", is.Name, err)
	}
	if store.ReservedIdentity(is.Name) {
		return fmt.Errorf("name %q is reserved", is.Name)
	}
	if !absoluteURL(is.Issuer) {
		return fmt.Errorf("issuer must be an absolute URL, got %q", is.Issuer)
	}
	if !absoluteURL(is.JWKSURI) {
		return fmt.Errorf("jwks_uri must be an absolute URL, got %q", is.JWKSURI)
	}
	if is.Audience == "" {
		return errors.New("audience is required")
	}
	for _, a := range is.Algorithms {
		if !slices.Contains([]string{"EdDSA", "ES256", "RS256"}, a) {
			return fmt.Errorf("algorithm %q is not one of EdDSA, ES256, RS256", a)
		}
	}
	if is.HumanTokenLifetimeSeconds < 0 || is.HumanTokenLifetimeSeconds > 86400 {
		return fmt.Errorf("human_token_lifetime_seconds must be 0 (the default, 900) or between 1 and 86400, got %d", is.HumanTokenLifetimeSeconds)
	}
	return nil
}
