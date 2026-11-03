//go:build !regtest
// +build !regtest

package main

import (
	"github.com/cosmos/cosmos-sdk/server"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
)

// startCmdOptions returns the SDK's default start command behaviour.
func startCmdOptions(addStartFlags servertypes.ModuleInitFlags) server.StartCmdOptions {
	return server.StartCmdOptions{AddFlags: addStartFlags}
}
