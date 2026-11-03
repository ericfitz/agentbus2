//go:build hashivault

package cmd

import "github.com/ericfitz/agentmesh/internal/keyprovider/hashivault"

func init() { keyBackends["hashivault"] = hashivault.New }
