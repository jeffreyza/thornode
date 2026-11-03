//go:build regtest
// +build regtest

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	cmtcfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/node"
	"github.com/cometbft/cometbft/p2p"
	pvm "github.com/cometbft/cometbft/privval"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/rpc/client/local"
	cmttypes "github.com/cometbft/cometbft/types"
	"golang.org/x/sync/errgroup"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/cosmos/cosmos-sdk/server/api"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
	servercmtlog "github.com/cosmos/cosmos-sdk/server/log"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"

	thorapp "gitlab.com/thorchain/thornode/v3/app"
)

// startCmdOptions starts the regression-test node through regtestStart.
func startCmdOptions(addStartFlags servertypes.ModuleInitFlags) server.StartCmdOptions {
	return server.StartCmdOptions{
		AddFlags:            addStartFlags,
		StartCommandHandler: regtestStart,
	}
}

// regtestStart is the SDK's in-process start (server.startInProcess) with two
// changes for the regression-test harness, which drives block production over
// HTTP (see app/app_regtest.go):
//
//   - The ABCI connections get one mutex each (NewConnSyncLocalClientCreator)
//     instead of one shared mutex, so the node parked in PrepareProposal waiting
//     for the harness still serves queries and CheckTx.
//   - Consensus never waits on wall-clock timeouts between blocks: timeout_commit
//     is REGTEST_TIMEOUT_COMMIT (default 0) and timeout_propose is effectively
//     infinite, so a proposal parked for the harness cannot time out into a new
//     round.
//
// gRPC-only and stand-alone modes are not supported here; regtest never uses them.
func regtestStart(svrCtx *server.Context, clientCtx client.Context, appCreator servertypes.AppCreator, withCmt bool, opts server.StartCmdOptions) error {
	if !withCmt {
		return fmt.Errorf("regtest start requires in-process CometBFT")
	}
	svrCfg, err := serverconfig.GetConfig(svrCtx.Viper)
	if err != nil {
		return err
	}
	if err = svrCfg.ValidateBasic(); err != nil {
		return err
	}

	db, err := opts.DBOpener(svrCtx.Config.RootDir, server.GetAppDBBackend(svrCtx.Viper))
	if err != nil {
		return err
	}
	app := appCreator(svrCtx.Logger, db, nil, svrCtx.Viper)
	defer func() {
		if cerr := app.Close(); cerr != nil {
			svrCtx.Logger.Error(cerr.Error())
		}
	}()

	cmtCfg := svrCtx.Config
	cmtCfg.Consensus.TimeoutCommit = 0
	if v := os.Getenv("REGTEST_TIMEOUT_COMMIT"); v != "" {
		if cmtCfg.Consensus.TimeoutCommit, err = time.ParseDuration(v); err != nil {
			return fmt.Errorf("bad REGTEST_TIMEOUT_COMMIT: %w", err)
		}
	}
	cmtCfg.Consensus.TimeoutPropose = 24 * time.Hour
	cmtCfg.Consensus.TimeoutProposeDelta = 0

	ctx, cancel := context.WithCancel(context.Background())
	g, ctx := errgroup.WithContext(ctx)
	server.ListenForQuitSignals(g, true, cancel, svrCtx.Logger)

	nodeKey, err := p2p.LoadOrGenNodeKey(cmtCfg.NodeKeyFile())
	if err != nil {
		return err
	}
	tmNode, err := node.NewNodeWithContext(
		ctx,
		cmtCfg,
		pvm.LoadOrGenFilePV(cmtCfg.PrivValidatorKeyFile(), cmtCfg.PrivValidatorStateFile()),
		nodeKey,
		proxy.NewConnSyncLocalClientCreator(server.NewCometABCIWrapper(app)),
		regtestGenDocProvider(cmtCfg),
		cmtcfg.DefaultDBProvider,
		node.DefaultMetricsProvider(cmtCfg.Instrumentation),
		servercmtlog.CometLoggerWrapper{Logger: svrCtx.Logger},
	)
	if err != nil {
		return err
	}
	thorapp.SetRegtestMempoolReaper(func(maxBytes, maxGas int64) [][]byte {
		return tmNode.Mempool().ReapMaxBytesMaxGas(maxBytes, maxGas).ToSliceOfBytes()
	})
	if err = tmNode.Start(); err != nil {
		return err
	}
	defer func() {
		if tmNode.IsRunning() {
			_ = tmNode.Stop()
		}
	}()

	if svrCfg.API.Enable || svrCfg.GRPC.Enable {
		clientCtx = clientCtx.WithClient(local.New(tmNode))
		app.RegisterTxService(clientCtx)
		app.RegisterTendermintService(clientCtx)
		app.RegisterNodeService(clientCtx, svrCfg)
	}

	// regtest serves the REST API only (no gRPC server)
	if svrCfg.API.Enable {
		apiSrv := api.New(clientCtx.WithHomeDir(cmtCfg.RootDir), svrCtx.Logger.With("module", "api-server"), nil)
		app.RegisterAPIRoutes(apiSrv, svrCfg.API)
		g.Go(func() error { return apiSrv.Start(ctx, svrCfg) })
	}

	return g.Wait()
}

func regtestGenDocProvider(cfg *cmtcfg.Config) func() (*cmttypes.GenesisDoc, error) {
	return func() (*cmttypes.GenesisDoc, error) {
		appGenesis, err := genutiltypes.AppGenesisFromFile(cfg.GenesisFile())
		if err != nil {
			return nil, err
		}
		return appGenesis.ToGenesisDoc()
	}
}
