package app

import (
	"os"
	"sync"

	"cosmossdk.io/log"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/client/flags"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"

	"gitlab.com/thorchain/thornode/v3/app/params"
)

const (
	TestApp = "testApp"
)

var (
	encodingConfigOnce   sync.Once
	cachedEncodingConfig params.EncodingConfig
)

// MakeEncodingConfig returns the process-wide EncodingConfig, building it exactly
// once and caching the result. Building it instantiates a temporary app which
// registers types into global amino/crypto codec registries; that registration is
// NOT concurrency-safe, so it must happen once and complete before any concurrent
// codec use (tx broadcasts, key decryption). Callers that fan out goroutines should
// warm this up first by calling it once single-threaded (see the simulation's
// InitConfig). Caching also avoids rebuilding an entire app on every call (the
// thorchain bridge previously rebuilt one per broadcast). Callers — the bifrost
// bridge, thorscan, and the test harnesses — must treat the returned config as
// read-only: its codec/registry fields are now shared process-wide, so mutating
// them (e.g. registering more types) would corrupt every other caller.
func MakeEncodingConfig() params.EncodingConfig {
	encodingConfigOnce.Do(func() {
		cachedEncodingConfig = makeEncodingConfigUncached()
	})
	return cachedEncodingConfig
}

func makeEncodingConfigUncached() params.EncodingConfig {
	dir, err := os.MkdirTemp("", "temp_bifrost")
	if err != nil {
		panic("failed to create temp_bifrost dir: " + err.Error())
	}
	defer os.RemoveAll(dir)
	// we "pre"-instantiate the application for getting the injected/configured encoding configuration
	// note, this is not necessary when using app wiring, as depinject can be directly used (see root_v2.go)
	tempApp := NewChainApp(
		log.NewNopLogger(),
		dbm.NewMemDB(),
		nil,
		false,
		NewTestAppOptionsWithFlagHome(dir),
		[]wasmkeeper.Option{},
	)
	return makeEncodingConfig(tempApp)
}

func makeEncodingConfig(tempApp *THORChainApp) params.EncodingConfig {
	encodingConfig := params.EncodingConfig{
		InterfaceRegistry: tempApp.InterfaceRegistry(),
		Codec:             tempApp.AppCodec(),
		TxConfig:          tempApp.TxConfig(),
		Amino:             tempApp.LegacyAmino(),
	}
	return encodingConfig
}

func NewTestAppOptionsWithFlagHome(homePath string) servertypes.AppOptions {
	return simtestutil.AppOptionsMap{
		flags.FlagHome: homePath,
		TestApp:        true,
	}
}
