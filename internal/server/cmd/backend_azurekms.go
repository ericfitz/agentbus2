//go:build azurekms

package cmd

import "github.com/ericfitz/agentmesh/internal/keyprovider/azurekms"

func init() { keyBackends["azurekms"] = azurekms.New }
