//go:build regtest
// +build regtest

package app

import (
	"net/http"
	"os"
	"strconv"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/rs/zerolog/log"

	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// The harness creates a block per /newBlock request. Block production is parked
// in PrepareProposal, before the mempool is reaped, so every tx the harness sent
// before the request is in the block, and /newBlock returns once the block is
// committed. Between blocks no ABCI call is held open, and the regtest start
// command (cmd/thornode/start_regtest.go) gives each ABCI connection its own
// lock, so queries and CheckTx never wait on block production.
var (
	begin = make(chan struct{})
	end   = make(chan struct{})
)

// regtestReapMempool reaps the CometBFT mempool; set by the regtest start command.
var regtestReapMempool func(maxBytes, maxGas int64) [][]byte

// SetRegtestMempoolReaper lets the regtest start command hand over its node's mempool.
func SetRegtestMempoolReaper(reap func(maxBytes, maxGas int64) [][]byte) {
	regtestReapMempool = reap
}

func wrapPrepareProposal(h sdk.PrepareProposalHandler) sdk.PrepareProposalHandler {
	return func(ctx sdk.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
		<-begin
		// CometBFT reaped the mempool before calling PrepareProposal, possibly
		// before the harness sent this block's txs. Reap again now, with the
		// limits CometBFT used (CreateProposalBlock), so the block holds every
		// tx sent before /newBlock.
		if regtestReapMempool != nil {
			maxReapBytes := req.MaxTxBytes
			var maxGas int64 = -1
			if params := ctx.ConsensusParams(); params.Block != nil {
				if params.Block.MaxBytes == -1 {
					maxReapBytes = -1
				}
				maxGas = params.Block.MaxGas
			}
			req.Txs = regtestReapMempool(maxReapBytes, maxGas)
		}
		return h(ctx, req)
	}
}

// setPrepareCheckStater signals /newBlock after Commit has persisted the block.
func setPrepareCheckStater(bApp *baseapp.BaseApp) {
	bApp.SetPrepareCheckStater(func(sdk.Context) { end <- struct{}{} })
}

func init() {
	count := 0

	// start an http server to unblock a block creation when a request is received
	newBlock := func(w http.ResponseWriter, r *http.Request) {
		begin <- struct{}{}
		<-end
		count++
		w.Write([]byte(strconv.Itoa(count)))
	}
	http.HandleFunc("/newBlock", newBlock)
	portString := os.Getenv("CREATE_BLOCK_PORT")
	go func() {
		err := http.ListenAndServe(":"+portString, nil)
		if err != nil {
			log.Fatal().Err(err).Msg("fail to start http server")
		}
	}()
}

func (app *THORChainApp) BeginBlocker(ctx sdk.Context) (sdk.BeginBlock, error) {
	// Use an artificial timestamp, to ensure block time specific changes
	// are consistent and don't change block hashes (eg. 24h volume calculation)
	timestamp := time.Unix(ctx.BlockHeight(), 0).UTC()
	return app.ModuleManager.BeginBlock(ctx.WithBlockTime(timestamp))
}

// EndBlocker application updates every end block
func (app *THORChainApp) EndBlocker(ctx sdk.Context) (sdk.EndBlock, error) {
	// Use an artificial timestamp, to ensure block time specific changes
	// are consistent and don't change block hashes (eg. 24h volume calculation)
	timestamp := time.Unix(ctx.BlockHeight(), 0).UTC()
	return app.ModuleManager.EndBlock(ctx.WithBlockTime(timestamp))
}
