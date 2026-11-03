//go:build awskms

package cmd

import "github.com/ericfitz/agentmesh/internal/keyprovider/awskms"

func init() { keyBackends["awskms"] = awskms.New }
