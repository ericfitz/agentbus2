//go:build azurekms

package cmd

import "github.com/ericfitz/agentbus2/internal/keyprovider/azurekms"

func init() { keyBackends["azurekms"] = azurekms.New }
