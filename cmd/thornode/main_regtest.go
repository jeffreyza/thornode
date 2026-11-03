//go:build regtest
// +build regtest

package main

import (
	"fmt"
	"os"
	"os/signal"
	"runtime/pprof"
	"syscall"

	svrcmd "github.com/cosmos/cosmos-sdk/server/cmd"

	"gitlab.com/thorchain/thornode/v3/app"
)

func main() {
	// opt-in CPU profile for perf analysis, one file per process
	if path := os.Getenv("THORNODE_CPUPROFILE"); path != "" {
		f, err := os.Create(fmt.Sprintf("%s.%d.pprof", path, os.Getpid()))
		if err == nil && pprof.StartCPUProfile(f) == nil {
			defer func() {
				pprof.StopCPUProfile()
				f.Close()
			}()
		}
	}

	// for coverage data we need to exit main without allowing the server to call os.Exit
	syn := make(chan error)
	go func() {
		rootCmd := NewRootCmd()
		syn <- svrcmd.Execute(rootCmd, "", app.DefaultNodeHome)
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGUSR1)
	select {
	case <-sig:
	case err := <-syn:
		if err != nil {
			pprof.StopCPUProfile()
			os.Exit(1)
		}
	}
}
