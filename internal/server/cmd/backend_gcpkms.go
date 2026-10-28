//go:build gcpkms

package cmd

import "github.com/ericfitz/agentbus2/internal/keyprovider/gcpkms"

func init() { keyBackends["gcpkms"] = gcpkms.New }
