//go:build gcpkms

package cmd

import "github.com/ericfitz/agentmesh/internal/keyprovider/gcpkms"

func init() { keyBackends["gcpkms"] = gcpkms.New }
