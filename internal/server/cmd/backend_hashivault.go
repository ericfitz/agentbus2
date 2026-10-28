//go:build hashivault

package cmd

import "github.com/ericfitz/agentbus2/internal/keyprovider/hashivault"

func init() { keyBackends["hashivault"] = hashivault.New }
