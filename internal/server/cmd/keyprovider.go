package cmd

import (
	"fmt"
	"time"

	"github.com/ericfitz/agentbus2/internal/core/signingkeys"
	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/server/config"
)

// keyBackends are the external key providers this build contains, by key URI
// scheme. A backend's tagged file registers itself from an init; the default
// build has none.
//
// open note: signing-keys 2
var keyBackends = map[string]func(uri string) (keyprovider.Provider, error){}

// openKeyProvider is the provider cfg.SigningKeyURI names: "" is the
// Store-row provider. A scheme whose backend is not in this build is refused
// with the build tag that adds it. config.Validate has already limited the
// scheme to keyprovider.Schemes.
func openKeyProvider(cfg config.Config) (keyprovider.Provider, error) {
	scheme, err := keyprovider.SchemeOf(cfg.SigningKeyURI)
	if err != nil {
		return nil, fmt.Errorf("signing_key_uri: %w", err)
	}
	if scheme == "" {
		return keyprovider.StoreProvider{}, nil
	}
	open, ok := keyBackends[scheme]
	if !ok {
		return nil, fmt.Errorf("signing_key_uri: the %s backend is not in this build (build with -tags %s)", scheme, scheme)
	}
	return open(cfg.SigningKeyURI)
}

// signingSettings maps the rotation settings; 0 hours turns scheduled
// rotation off.
func signingSettings(cfg config.Config) signingkeys.Settings {
	return signingkeys.Settings{
		Interval: time.Duration(cfg.SigningKeyRotationHours) * time.Hour,
		Cooldown: time.Duration(cfg.SigningKeyRotationCooldownSeconds) * time.Second,
	}
}
