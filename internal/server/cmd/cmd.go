package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/embed"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/core/inspect"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/core/notify/pgnotify"
	"github.com/ericfitz/agentbus2/internal/core/signingkeys"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/server"
	"github.com/ericfitz/agentbus2/internal/server/config"
	"github.com/ericfitz/agentbus2/internal/slackapi"
	"github.com/ericfitz/agentbus2/internal/slackapi/slackgo"
	"github.com/ericfitz/agentbus2/internal/slackbridge"
)

// Run is `agentbus2 server` (Decision 85): the configuration comes from the
// -config file, the AGENTBUS2_SERVER_* variables in environ and the flags,
// and `server admin ...` dispatches to RunAdmin. Exit codes: 0 clean shutdown
// or -h, 1 runtime error, 2 flag or configuration error. Ported from v1
// main.go (run's subcommand dispatch, flag.ContinueOnError, parseExit).
func Run(ctx context.Context, args []string, environ []string, stdout, stderr io.Writer) int {
	return run(ctx, args, environ, stdout, stderr, Serve)
}

// run is Run with the serving function injected, so a test can look at the
// Config the flags produce without serving.
//
// State (Definition of Done D): the signing key set is loaded from, or its
// first key created in, the Store (signingkeys.Service.EnsureActive). A second
// start loads the active row and prints nothing; two starts at once end with
// one row and one of them prints "generated signing key" (or "adopted signing
// key" for an external provider); a start that fails afterwards leaves the
// committed row for the next start; a corrupt row stops the start (exit 1)
// and is never overwritten; an active row of another provider than the
// configured one stops the start (exit 1, the message names the rotate
// command) and is never changed. A key service that is down at start with no
// active row exits 1, and the next start retries.
func run(ctx context.Context, args []string, environ []string, stdout, stderr io.Writer, serve func(context.Context, Config) error) int {
	if len(args) > 0 && args[0] == "admin" {
		return RunAdmin(ctx, args[1:], environ, stdout, stderr)
	}
	var f flags
	fs := flagSet("agentbus2 server", &f, true)
	var usage bytes.Buffer
	fs.SetOutput(&usage)
	if code, done := parseFlags(fs, args, &usage, stdout, stderr); done {
		return code
	}
	fail := func(code int, format string, a ...any) int {
		_, _ = fmt.Fprintf(stderr, "agentbus2 server: "+format+"\n", a...)
		return code
	}
	if fs.NArg() > 0 {
		return fail(2, "unexpected argument %q", fs.Arg(0))
	}
	cfg, cc, err := loadConfig(&f, fs, environ)
	if err != nil {
		return fail(2, "%v", err)
	}
	if cfg.Listen == "" {
		return fail(2, "listen is required, for example 127.0.0.1:0 (set listen)")
	}
	local, err := ClassifyListen(cfg.Listen)
	if err != nil {
		return fail(2, "%v", err)
	}
	// An unknown registration value never reaches Serve: ParseMode rejects it
	// here as well as in config.Validate (the register handler's refusal of an
	// unset or unrecognized mode is only a backstop).
	var mode identities.Mode
	switch {
	case cfg.Registration != "":
		if mode, err = identities.ParseMode(cfg.Registration); err != nil {
			return fail(2, "registration: %v", err)
		}
	case local:
		mode = identities.ModeOpen
	default:
		mode = identities.ModeEnrollment
	}
	// The socket path is not a host, so a unix listener has no default issuer.
	if cfg.Issuer == "" && strings.HasPrefix(cfg.Listen, "unix:") {
		return fail(2, "issuer is required when listen is a unix socket (set issuer)")
	}

	logger := newLogger(cfg, stderr)
	for _, w := range cfg.Warnings() {
		logger.Warn(w)
	}

	// The Slack tokens are read before the store is opened, so a bad file
	// changes nothing.
	slackClient, err := slackClientFor(cfg, logger)
	if err != nil {
		return fail(2, "%v", err)
	}

	st, err := openStore(ctx, cfg, logger)
	if err != nil {
		return fail(1, "%v", err)
	}
	defer func() { _ = st.Close() }()

	// Bind before building the issuer: the default issuer URL is the bound address.
	ln, err := Listen(cfg.Listen)
	if err != nil {
		return fail(1, "listen: %v", err)
	}
	defer func() { _ = ln.Close() }() // Serve closes it too; a second Close is harmless

	issuerURL := cfg.Issuer
	if issuerURL == "" {
		issuerURL = "http://" + boundAuthority(cfg.Listen, ln.Addr())
		if !local {
			_, _ = fmt.Fprintf(stdout, "issuer is unset: clients will see %s in openid-configuration (set issuer)\n", issuerURL)
		}
	}

	// The signing key is a Store row (Milestone 5 choice 6). This Core exists
	// only for it; Serve builds its own over the same Store.
	c, err := core.New(st, cc, core.Options{Now: time.Now, Logger: logger})
	if err != nil {
		return fail(1, "%v", err)
	}
	provider, err := openKeyProvider(cfg)
	if err != nil {
		// A key service that cannot be reached is a runtime failure (1); a
		// bad URI or a backend not in this build is a configuration error (2).
		if errors.Is(err, keyprovider.ErrUnavailable) {
			return fail(1, "%v", err)
		}
		return fail(2, "%v", err)
	}
	keysSvc := &signingkeys.Service{Core: c, Provider: provider, Settings: signingSettings(cfg)}
	active, created, err := keysSvc.EnsureActive(ctx)
	if err != nil {
		return fail(1, "signing key: %v", err)
	}
	issuer, err := token.NewIssuer(ctx, issuerURL, signingkeys.NewKeyring(*keysSvc), time.Duration(cfg.TokenLifetimeSeconds)*time.Second, c.Now)
	if err != nil {
		return fail(1, "issuer: %v", err)
	}
	// Serve's maintenance jobs rotate through this Service, so a rotation
	// reloads this process's issuer at once.
	keysSvc.OnChange = issuer.Invalidate
	if created {
		if provider.Name() == keyprovider.NameStore {
			_, _ = fmt.Fprintf(stdout, "generated signing key %s\n", active.KID)
		} else {
			_, _ = fmt.Fprintf(stdout, "adopted signing key %s (%s %s)\n", active.KID, provider.Name(), keyprovider.KeyName(cfg.SigningKeyURI))
		}
	}

	ext := externalIssuers(cfg)

	var embedder core.Embedder
	if cfg.EmbeddingEndpoint != "" {
		key, err := apiKey("embedding_api_key_file", cfg.EmbeddingAPIKeyFile, "embedding_api_key_env", cfg.EmbeddingAPIKeyEnv, environ)
		if err != nil {
			return fail(2, "%v", err)
		}
		embedder = embed.NewHTTP(cfg.EmbeddingEndpoint, cfg.EmbeddingModel, key, nil)
	}

	var inspector core.Inspector
	if cfg.InspectionEndpoint != "" {
		key, err := apiKey("inspection_api_key_file", cfg.InspectionAPIKeyFile, "inspection_api_key_env", cfg.InspectionAPIKeyEnv, environ)
		if err != nil {
			return fail(2, "%v", err)
		}
		inspector = inspect.NewWebhook(cfg.InspectionEndpoint, key, nil)
	}

	notifier, closeNotifier := storeNotifier(ctx, cfg, logger)
	defer closeNotifier()

	engine := "postgres"
	if cfg.SQLite != "" {
		engine = "sqlite"
	}
	err = serve(ctx, Config{
		Listen:       cfg.Listen,
		Listener:     ln,
		Store:        st,
		Notifier:     notifier,
		Auth:         server.Bearer{Verifier: token.Verifier{Own: issuer}},
		Cfg:          &cc,
		Logger:       logger,
		Registration: mode,
		Issuer:       issuer,
		SigningKeys:  keysSvc,
		External:     ext,
		Embedder:     embedder,
		Inspector:    inspector,
		Slack:        slackClient,
		SlackCfg: slackbridge.Config{
			ArchiveAfterDone: time.Duration(cfg.SlackArchiveAfterDoneHours) * time.Hour,
			MaxMessageChars:  cfg.SlackMaxMessageChars,
			Logger:           logger,
		},
		Ready: func(a net.Addr) {
			_, _ = fmt.Fprintf(stdout, "agentbus2 server listening on %s (%s, registration %s, issuer %s, kid %s)\n",
				a, engine, mode, issuer.URL(), issuer.KID())
		},
	})
	if err != nil {
		return fail(1, "%v", err)
	}
	return 0
}

// storeNotifier is the Notifier Serve gets for cfg's store, and the function
// that ends it. A Postgres store shares hints across replicas through
// LISTEN/NOTIFY (Decision 52), so it gets a pgnotify listener that lives as
// long as Run; SQLite gets nil, Serve's in-process Memory (Milestone 4 choice
// 14: two servers over one SQLite file do not share hints).
func storeNotifier(ctx context.Context, cfg config.Config, logger *slog.Logger) (notify.Notifier, func()) {
	if cfg.Postgres == "" {
		return nil, func() {}
	}
	l := pgnotify.New(ctx, cfg.Postgres, pgnotify.Options{Logger: logger})
	return l, l.Close
}

// boundAuthority is the bound address as a URL authority. A wildcard listen
// address binds dual-stack, and the kernel then reports "[::]:port" even for
// "0.0.0.0:port"; the configured literal host is kept in that case, so the
// URL (and the warning about it) names what the operator typed.
func boundAuthority(listen string, bound net.Addr) string {
	host, port, err := net.SplitHostPort(bound.String())
	if err != nil {
		return bound.String()
	}
	if cfgHost, _, err := net.SplitHostPort(listen); err == nil && cfgHost != "" {
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			host = cfgHost
		}
	}
	return net.JoinHostPort(host, port)
}

// logLevel maps the validated log_level setting.
func logLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// externalIssuers maps cfg.Issuers onto token.External. An unset claim name or
// human lifetime stays empty: token.External applies the defaults (spec 5.1).
// Validate has already limited
// algorithms to EdDSA, ES256 and RS256; an empty list keeps the verifier's
// default of all three.
func externalIssuers(cfg config.Config) []*token.External {
	var out []*token.External
	for _, i := range cfg.Issuers {
		var algs []jose.SignatureAlgorithm
		for _, a := range i.Algorithms {
			algs = append(algs, jose.SignatureAlgorithm(a))
		}
		out = append(out, &token.External{
			Name: i.Name, Issuer: i.Issuer, JWKSURI: i.JWKSURI, Audience: i.Audience,
			Algorithms: algs,
			Claims: token.ClaimNames{
				Email:   i.Claims.Email,
				Groups:  i.Claims.Groups,
				Subject: i.Claims.Subject,
			},
			HumanLifetime: time.Duration(i.HumanTokenLifetimeSeconds) * time.Second,
			TTL:           time.Duration(cfg.JWKSRefreshSeconds) * time.Second,
		})
	}
	return out
}

// apiKey reads an API key from the file the setting fileSetting names (file)
// or from the variable the setting envSetting names (env) in environ; neither
// set means no key. Validate allows at most one of the two. An error names the
// setting, never the key.
func apiKey(fileSetting, file, envSetting, env string, environ []string) (string, error) {
	switch {
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("%s: %w", fileSetting, err)
		}
		return strings.TrimSpace(string(b)), nil
	case env != "":
		prefix := env + "="
		val := ""
		for _, kv := range environ {
			if v, ok := strings.CutPrefix(kv, prefix); ok {
				val = v
			}
		}
		if val == "" {
			return "", fmt.Errorf("%s names %s, which is not set", envSetting, env)
		}
		return strings.TrimSpace(val), nil
	}
	return "", nil
}

// slackClientFor reads the two Slack token files and builds the adapter; nil
// when no Slack setting is present (the bridge is off). Validate has already
// required both files or neither. An error names the setting, never a token or
// any file content.
func slackClientFor(cfg config.Config, logger *slog.Logger) (slackapi.Client, error) {
	if cfg.SlackAppTokenFile == "" {
		return nil, nil
	}
	read := func(setting, path string) (string, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			var pe *os.PathError
			if errors.As(err, &pe) {
				err = pe.Err // the path is the operator's own; the reason is enough
			}
			return "", fmt.Errorf("%s: %w", setting, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	app, err := read("slack_app_token_file", cfg.SlackAppTokenFile)
	if err != nil {
		return nil, err
	}
	bot, err := read("slack_bot_token_file", cfg.SlackBotTokenFile)
	if err != nil {
		return nil, err
	}
	return slackgo.New(app, bot, slackgo.Options{Logger: logger, HTTP: &http.Client{Timeout: slackHTTPTimeout}})
}

// slackHTTPTimeout bounds every Slack Web API call, so a Slack that accepts
// connections and never answers cannot block the start-up auth.test.
const slackHTTPTimeout = 30 * time.Second
