//go:build awskms

package cmd

import "github.com/ericfitz/agentbus2/internal/keyprovider/awskms"

func init() { keyBackends["awskms"] = awskms.New }
