package thorchain

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"cosmossdk.io/core/appmodule"
	"github.com/blang/semver"
	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	cdctypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/server/api"
	"github.com/cosmos/cosmos-sdk/telemetry"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkgrpc "github.com/cosmos/cosmos-sdk/types/grpc"
	"github.com/cosmos/cosmos-sdk/types/module"
	gateway "github.com/cosmos/gogogateway"
	"github.com/gorilla/mux"
	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	"github.com/hashicorp/go-metrics"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/metadata"

	"gitlab.com/thorchain/thornode/v3/app/params"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tcysmartcontract"
	"gitlab.com/thorchain/thornode/v3/constants"

	"gitlab.com/thorchain/thornode/v3/x/thorchain/client/cli"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

// type check to ensure the interface is properly implemented
var (
	_ module.AppModule           = AppModule{}
	_ module.AppModuleBasic      = AppModuleBasic{}
	_ module.AppModuleGenesis    = AppModule{}
	_ module.HasABCIGenesis      = AppModule{}
	_ module.HasServices         = AppModule{}
	_ module.HasABCIEndBlock     = AppModule{}
	_ module.HasConsensusVersion = AppModule{}
	_ appmodule.HasBeginBlocker  = AppModule{}
)

// AppModuleBasic app module Basics object
type AppModuleBasic struct{}

// Name return the module's name
func (AppModuleBasic) Name() string {
	return ModuleName
}

// RegisterLegacyAminoCodec registers the module's types for the given codec.
func (AppModuleBasic) RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {
	RegisterLegacyAminoCodec(cdc)
}

// RegisterInterfaces registers the module's interface types
func (a AppModuleBasic) RegisterInterfaces(reg cdctypes.InterfaceRegistry) {
	RegisterInterfaces(reg)
}

// DefaultGenesis returns default genesis state as raw bytes for the module.
func (AppModuleBasic) DefaultGenesis(cdc codec.JSONCodec) json.RawMessage {
	return cdc.MustMarshalJSON(DefaultGenesis())
}

// ValidateGenesis check of the Genesis
func (AppModuleBasic) ValidateGenesis(cdc codec.JSONCodec, config client.TxEncodingConfig, bz json.RawMessage) error {
	var data GenesisState
	if err := cdc.UnmarshalJSON(bz, &data); err != nil {
		return err
	}
	// Once json successfully marshalled, passes along to genesis.go
	return ValidateGenesis(data)
}

// RegisterGRPCGatewayRoutes registers the gRPC Gateway routes for the mint module.
// thornode current doesn't have grpc endpoint yet
func (AppModuleBasic) RegisterGRPCGatewayRoutes(clientCtx client.Context, mux *runtime.ServeMux) {
	if err := types.RegisterQueryHandlerClient(context.Background(), mux, types.NewQueryClient(clientCtx)); err != nil {
		panic(err)
	}
}

// GetQueryCmd get the root query command of this module
func (AppModuleBasic) GetQueryCmd() *cobra.Command {
	return cli.GetQueryCmd()
}

// GetTxCmd get the root tx command of this module
func (AppModuleBasic) GetTxCmd() *cobra.Command {
	return cli.GetTxCmd()
}

// ____________________________________________________________________________

// AppModule implements an application module for the thorchain module.
type AppModule struct {
	AppModuleBasic
	mgr              *Mgrs
	telemetryEnabled bool
	msgServer        types.MsgServer
	queryServer      types.QueryServer
}

// NewAppModule creates a new AppModule Object
func NewAppModule(
	mgr *Mgrs,
	telemetryEnabled bool,
	testApp bool,
) AppModule {
	kb := cosmos.KeybaseStore{}
	var err error
	if !testApp {
		kb, err = cosmos.GetKeybase(os.Getenv(cosmos.EnvChainHome))
		if err != nil {
			panic(err)
		}
	}
	txConfig, err := params.TxConfig(mgr.cdc, nil)
	if err != nil {
		panic(fmt.Errorf("failed to create tx config: %w", err))
	}
	return AppModule{
		AppModuleBasic:   AppModuleBasic{},
		mgr:              mgr,
		telemetryEnabled: telemetryEnabled,
		msgServer:        NewMsgServerImpl(mgr),
		queryServer:      NewQueryServerImpl(mgr, txConfig, kb),
	}
}

func (AppModule) IsAppModule() {}

func (AppModule) IsOnePerModuleType() {}

func (AppModule) ConsensusVersion() uint64 {
	return 21
}

func (am AppModule) RegisterInvariants(_ sdk.InvariantRegistry) {}

func (am AppModule) QuerierRoute() string {
	return types.QuerierRoute
}

// RegisterServices registers module services.
func (am AppModule) RegisterServices(cfg module.Configurator) {
	types.RegisterMsgServer(cfg.MsgServer(), am.msgServer)
	types.RegisterQueryServer(cfg.QueryServer(), am.queryServer)
	wasmtypes.RegisterQueryServer(cfg.QueryServer(), wasmkeeper.Querier(&am.mgr.wasmKeeper))

	// Each migration runs in a report-only LUVI scope (luvi_migration.go);
	// register new ones in Migrator.luviMigrations.
	m := NewMigrator(am.mgr)
	for _, mig := range m.luviMigrations() {
		if err := cfg.RegisterMigration(types.ModuleName, mig.from, luviGuardMigration(am.mgr, mig.from, mig.run)); err != nil {
			panic(fmt.Sprintf("failed to migrate x/thorchain from version %d to %d: %v", mig.from, mig.from+1, err))
		}
	}
}

// BeginBlock called when a block get proposed
func (am AppModule) BeginBlock(goCtx context.Context) error {
	ctx := sdk.UnwrapSDKContext(goCtx)
	ctx = ctx.WithLogger(ctx.Logger().With("module", "x/"+ModuleName, "height", ctx.BlockHeight()))
	// BeginBlock lists the active validators several times; share one decode
	// until a node account is written. Not in DeliverTx: gas_used would change.
	ctx = keeper.WithActiveValidatorsCache(ctx)

	votes := ctx.CometInfo().GetLastCommit().Votes()
	var existingValidators []string
	for i := range votes.Len() {
		v := votes.Get(i)
		addr := sdk.ValAddress(v.Validator().Address())
		existingValidators = append(existingValidators, addr.String())
	}

	ctx.Logger().Debug("BeginBlock")
	// Check/Update the network version before checking the local version
	if err := am.mgr.LoadManagerIfNecessary(ctx); err != nil {
		ctx.Logger().Error("fail to get managers", "error", err)
	}

	version := am.mgr.GetVersion()
	localVer := semver.MustParse(constants.SWVersion.String())
	if version.Major > localVer.Major || version.Minor > localVer.Minor {
		panic(fmt.Sprintf("Unsupported Version: update your binary (your version: %s, network consensus version: %s)", constants.SWVersion.String(), version.String()))
	}

	// Clear all oracle prices
	if err := am.mgr.OracleManager().BeginBlock(ctx); err != nil {
		ctx.Logger().Error("fail to process oracle manager", "error", err)
	}

	am.mgr.Keeper().ClearObservingAddresses(ctx)

	am.mgr.GasMgr().BeginBlock()
	if err := am.mgr.NetworkMgr().BeginBlock(ctx, am.mgr); err != nil {
		ctx.Logger().Error("fail to begin network manager", "error", err)
	}
	am.mgr.Slasher().BeginBlock(ctx, am.mgr.GetConstants())
	if err := am.mgr.ValidatorMgr().BeginBlock(ctx, am.mgr, existingValidators); err != nil {
		ctx.Logger().Error("Fail to begin block on validator", "error", err)
	}

	if err := am.mgr.Keeper().RemoveExpiredUpgradeProposals(ctx); err != nil {
		ctx.Logger().Error("Failed to remove expired upgrade proposals", "error", err)
	}

	return nil
}

// EndBlock called when a block get committed
func (am AppModule) EndBlock(goCtx context.Context) ([]abci.ValidatorUpdate, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	ctx = ctx.WithLogger(ctx.Logger().With("module", "x/"+ModuleName, "height", ctx.BlockHeight()))
	// EndBlock lists the active validators many times (per POL pool, per
	// outbound, per manager); share one decode until a node account is written.
	ctx = keeper.WithActiveValidatorsCache(ctx)

	ctx.Logger().Debug("End Block")

	// Per-manager timing and error counters below are leaf, process-local
	// telemetry: telemetry.Now() and ModuleMeasureSince are no-ops when the SDK
	// telemetry sink is disabled, and neither reads nor writes keeper state. The
	// order of the manager calls and their error handling are unchanged.
	start := telemetry.Now()
	luviCtx, luviStep := beginLuviScope(ctx, am.mgr.Keeper(), "endblock/swap_queue", "", false)
	if err := am.mgr.SwapQ().EndBlock(luviCtx, am.mgr); err != nil {
		ctx.Logger().Error("fail to process swap queue", "error", err)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "endblock", "manager_error"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("manager", "swap_queue")},
		)
	}
	luviStep.end(luviCtx, am.mgr)
	telemetry.ModuleMeasureSince("swap_queue", start, "thornode", "block", "endblock")

	start = telemetry.Now()
	luviCtx, luviStep = beginLuviScope(ctx, am.mgr.Keeper(), "endblock/adv_swap_queue", "", false)
	if err := am.mgr.AdvSwapQueueMgr().EndBlock(luviCtx, am.mgr, am.telemetryEnabled); err != nil {
		ctx.Logger().Error("fail to process adv swap queue", "error", err)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "endblock", "manager_error"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("manager", "adv_swap_queue")},
		)
	}
	luviStep.end(luviCtx, am.mgr)
	telemetry.ModuleMeasureSince("adv_swap_queue", start, "thornode", "block", "endblock")

	start = telemetry.Now()
	luviCtx, luviStep = beginLuviScope(ctx, am.mgr.Keeper(), "endblock/lack_signing", "", false)
	if err := am.mgr.Slasher().LackSigning(luviCtx, am.mgr); err != nil {
		ctx.Logger().Error("Unable to slash for lack of signing:", "error", err)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "endblock", "manager_error"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("manager", "lack_signing")},
		)
	}
	luviStep.end(luviCtx, am.mgr)
	telemetry.ModuleMeasureSince("lack_signing", start, "thornode", "block", "endblock")

	start = telemetry.Now()
	luviCtx, luviStep = beginLuviScope(ctx, am.mgr.Keeper(), "endblock/pool", "", false)
	if err := am.mgr.PoolMgr().EndBlock(luviCtx, am.mgr); err != nil {
		ctx.Logger().Error("fail to process pools", "error", err)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "endblock", "manager_error"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("manager", "pool")},
		)
	}
	luviStep.end(luviCtx, am.mgr)
	telemetry.ModuleMeasureSince("pool", start, "thornode", "block", "endblock")

	start = telemetry.Now()
	am.mgr.ObMgr().EndBlock(ctx, am.mgr.Keeper())
	telemetry.ModuleMeasureSince("observer", start, "thornode", "block", "endblock")

	// update network data to account for block rewards and reward units
	start = telemetry.Now()
	luviCtx, luviStep = beginLuviScope(ctx, am.mgr.Keeper(), "endblock/update_network", "", false)
	if err := am.mgr.NetworkMgr().UpdateNetwork(luviCtx, am.mgr); err != nil {
		ctx.Logger().Error("fail to update network data", "error", err)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "endblock", "manager_error"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("manager", "update_network")},
		)
	}
	luviStep.end(luviCtx, am.mgr)
	telemetry.ModuleMeasureSince("update_network", start, "thornode", "block", "endblock")

	start = telemetry.Now()
	luviCtx, luviStep = beginLuviScope(ctx, am.mgr.Keeper(), "endblock/stable_reserve", "", false)
	if err := am.mgr.StableReserveManager().EndBlock(luviCtx, am.mgr); err != nil {
		ctx.Logger().Error("fail to process stable reserve", "error", err)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "endblock", "manager_error"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("manager", "stable_reserve")},
		)
	}
	luviStep.end(luviCtx, am.mgr)
	telemetry.ModuleMeasureSince("stable_reserve", start, "thornode", "block", "endblock")

	start = telemetry.Now()
	luviCtx, luviStep = beginLuviScope(ctx, am.mgr.Keeper(), "endblock/network", "", false)
	if err := am.mgr.NetworkMgr().EndBlock(luviCtx, am.mgr); err != nil {
		ctx.Logger().Error("fail to end block for vault manager", "error", err)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "endblock", "manager_error"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("manager", "network")},
		)
	}
	luviStep.end(luviCtx, am.mgr)
	telemetry.ModuleMeasureSince("network", start, "thornode", "block", "endblock")

	start = telemetry.Now()
	if err := processObservationTallies(ctx, am.mgr); err != nil {
		ctx.Logger().Error("fail to process observation tallies", "error", err)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "endblock", "manager_error"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("manager", "observation_tally")},
		)
	}
	telemetry.ModuleMeasureSince("observation_tally", start, "thornode", "block", "endblock")

	start = telemetry.Now()
	luviCtx, luviStep = beginLuviScope(ctx, am.mgr.Keeper(), "endblock/validators", "", false)
	validators := am.mgr.ValidatorMgr().EndBlock(luviCtx, am.mgr)
	luviStep.end(luviCtx, am.mgr)
	telemetry.ModuleMeasureSince("validator", start, "thornode", "block", "endblock")

	start = telemetry.Now()
	luviCtx, luviStep = beginLuviScope(ctx, am.mgr.Keeper(), "endblock/txout", "", false)
	if err := am.mgr.TxOutStore().EndBlock(luviCtx, am.mgr); err != nil {
		ctx.Logger().Error("fail to process txout endblock", "error", err)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "endblock", "manager_error"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("manager", "txout")},
		)
	}
	luviStep.end(luviCtx, am.mgr)
	telemetry.ModuleMeasureSince("txout", start, "thornode", "block", "endblock")

	start = telemetry.Now()
	luviCtx, luviStep = beginLuviScope(ctx, am.mgr.Keeper(), "endblock/gas", "", false)
	am.mgr.GasMgr().EndBlock(luviCtx, am.mgr.Keeper(), am.mgr.EventMgr())
	luviStep.end(luviCtx, am.mgr)
	telemetry.ModuleMeasureSince("gas", start, "thornode", "block", "endblock")

	// telemetry
	if am.telemetryEnabled {
		start = telemetry.Now()
		if err := emitEndBlockTelemetry(ctx, am.mgr); err != nil {
			ctx.Logger().Error("unable to emit end block telemetry", "error", err)
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "endblock", "manager_error"},
				float32(1),
				[]metrics.Label{telemetry.NewLabel("manager", "telemetry")},
			)
		}
		telemetry.ModuleMeasureSince("telemetry", start, "thornode", "block", "endblock")
	}

	// update volumes
	start = telemetry.Now()
	if err := am.mgr.VolumeManager().EndBlock(ctx); err != nil {
		ctx.Logger().Error("fail update volumes", "error", err)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "endblock", "manager_error"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("manager", "volume")},
		)
	}
	telemetry.ModuleMeasureSince("volume", start, "thornode", "block", "endblock")

	start = telemetry.Now()
	if err := am.mgr.DynamicFeeMgr().EndBlock(ctx); err != nil {
		ctx.Logger().Error("fail to end block for dynamic fee manager", "error", err)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "endblock", "manager_error"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("manager", "dynamic_fee")},
		)
	}
	telemetry.ModuleMeasureSince("dynamic_fee", start, "thornode", "block", "endblock")

	start = telemetry.Now()
	if err := am.mgr.ScheduledMigrationManager().EndBlock(ctx, am.mgr); err != nil {
		ctx.Logger().Error("fail to process scheduled migration", "error", err)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "endblock", "manager_error"},
			float32(1),
			[]metrics.Label{telemetry.NewLabel("manager", "scheduled_migration")},
		)
	}
	telemetry.ModuleMeasureSince("scheduled_migration", start, "thornode", "block", "endblock")

	start = telemetry.Now()
	am.mgr.Keeper().PruneRefundCleanups(ctx)
	telemetry.ModuleMeasureSince("prune_refund_cleanups", start, "thornode", "block", "endblock")

	// last, so each accumulator step covers the whole block
	start = telemetry.Now()
	advanceLuviAccumulators(ctx, am.mgr.Keeper())
	telemetry.ModuleMeasureSince("luvi_accumulators", start, "thornode", "block", "endblock")

	return validators, nil
}

// InitGenesis initialise genesis
func (am AppModule) InitGenesis(ctx sdk.Context, cdc codec.JSONCodec, data json.RawMessage) []abci.ValidatorUpdate {
	var genState GenesisState
	ModuleCdc.MustUnmarshalJSON(data, &genState)
	return InitGenesis(ctx, am.mgr.Keeper(), genState)
}

// ExportGenesis export genesis
func (am AppModule) ExportGenesis(ctx sdk.Context, cdc codec.JSONCodec) json.RawMessage {
	gs := ExportGenesis(ctx, am.mgr.Keeper())
	return ModuleCdc.MustMarshalJSON(&gs)
}

// CustomGRPCGatewayRouter sets thorchain's custom GRPC gateway router
// Must be called before any GRPC gateway routes are registered
// GRPC gateway router settings are the same as cosmos sdk except for the additional
// serve mux option, WithMetadata().
func CustomGRPCGatewayRouter(apiSvr *api.Server) {
	clientCtx := apiSvr.ClientCtx

	// The default JSON marshaller used by the gRPC-Gateway is unable to marshal non-nullable non-scalar fields.
	// Using the gogo/gateway package with the gRPC-Gateway WithMarshaler option fixes the scalar field marshaling issue.
	marshalerOption := &gateway.JSONPb{
		EmitDefaults: true,
		Indent:       "",
		OrigName:     true,
		AnyResolver:  clientCtx.InterfaceRegistry,
	}

	apiSvr.GRPCGatewayRouter = runtime.NewServeMux(
		// Custom marshaler option is required for gogo proto
		runtime.WithMarshalerOption(runtime.MIMEWildcard, marshalerOption),

		// This is necessary to get error details properly
		// marshaled in unary requests.
		runtime.WithProtoErrorHandler(runtime.DefaultHTTPProtoErrorHandler),

		// Custom header matcher for mapping request headers to
		// GRPC metadata
		runtime.WithIncomingHeaderMatcher(api.CustomGRPCHeaderMatcher),

		// This is necessary to be able to use the height query param for setting the correct state.
		// Cosmos sdk expect the GRPCBlockHeightHeader to be set if the latest height is not used.
		// This function will extract the height query param and set it in the metadata for the sdk to consume.
		runtime.WithMetadata(func(ctx context.Context, req *http.Request) metadata.MD {
			md := make(metadata.MD, 1)
			for key := range req.Header {
				// if the GRPCBlockHeightHeader is set, use that and ignore the height query parameter
				if key == sdkgrpc.GRPCBlockHeightHeader {
					return md
				}
			}
			// The following checked endpoint prefixes have the height query parameter extracted.
			if strings.HasPrefix(req.URL.Path, "/thorchain/") ||
				strings.HasPrefix(req.URL.Path, "/cosmos/") ||
				strings.HasPrefix(req.URL.Path, "/bank/balances/") ||
				strings.HasPrefix(req.URL.Path, "/auth/accounts/") {
				heightStr, ok := req.URL.Query()["height"]
				if ok && len(heightStr) > 0 {
					_, err := strconv.ParseInt(heightStr[0], 10, 64)
					// if a valid int, set the GRPCBlockHeightHeader, the query server will error later on invalid height params
					if err == nil {
						md.Set(sdkgrpc.GRPCBlockHeightHeader, heightStr...)
					}
				}
			}
			return md
		}),
	)
}

// RegisterSupplyCMCRoute registers the /thorchain/supply/cmc plain-text HTTP handler.
func RegisterSupplyCMCRoute(router *mux.Router, clientCtx client.Context) {
	router.HandleFunc("/thorchain/supply/cmc", func(w http.ResponseWriter, r *http.Request) {
		queryClient := types.NewQueryClient(clientCtx)

		asset := strings.ToLower(r.URL.Query().Get("asset"))
		typ := strings.ToLower(r.URL.Query().Get("type"))

		var value int64
		switch asset {
		case "tcy":
			bankClient := banktypes.NewQueryClient(clientCtx)
			supplyResp, err := bankClient.SupplyOf(r.Context(), &banktypes.QuerySupplyOfRequest{Denom: "tcy"})
			if err != nil {
				http.Error(w, "failed to query tcy supply", http.StatusInternalServerError)
				return
			}
			total := supplyResp.Amount.Amount.Int64() / 1e8

			// sum staked TCY across the module account and wasm staking contracts
			var staked int64
			balResp, err := queryClient.BalanceModule(r.Context(), &types.QueryBalanceModuleRequest{Name: TCYStakeName})
			if err != nil {
				http.Error(w, "failed to query tcy stake module", http.StatusInternalServerError)
				return
			}
			for _, coin := range balResp.Coins {
				if coin.Denom == "tcy" {
					staked += coin.Amount.Int64()
				}
			}
			var addrBalResp *types.QueryBalancesResponse
			for _, addr := range tcysmartcontract.TCYSmartContractAddresses {
				addrBalResp, err = queryClient.Balances(r.Context(), &types.QueryBalancesRequest{Address: addr})
				if err != nil {
					continue // skip contracts that don't exist on this network
				}
				for _, bal := range addrBalResp.Balances {
					if bal.Denom == "tcy" {
						var amt int64
						amt, err = strconv.ParseInt(bal.Amount, 10, 64)
						if err == nil {
							staked += amt
						}
					}
				}
			}
			staked /= 1e8

			// unclaimed TCY in the claiming module
			var locked int64
			claimResp, err := queryClient.BalanceModule(r.Context(), &types.QueryBalanceModuleRequest{Name: TCYClaimingName})
			if err != nil {
				http.Error(w, "failed to query tcy claim module", http.StatusInternalServerError)
				return
			}
			for _, coin := range claimResp.Coins {
				if coin.Denom == "tcy" {
					locked = coin.Amount.Int64() / 1e8
				}
			}

			switch typ {
			case "total":
				value = total
			case "staked":
				value = staked
			case "locked":
				value = locked
			case "circulating":
				value = total - locked
			default:
				http.Error(w, "type must be total, staked, locked, or circulating", http.StatusBadRequest)
				return
			}
		case "rune", "":
			resp, err := queryClient.Supply(r.Context(), &types.QuerySupplyRequest{})
			if err != nil {
				http.Error(w, "failed to query rune supply", http.StatusInternalServerError)
				return
			}
			switch typ {
			case "circulating":
				value = resp.Circulating
			case "total":
				value = resp.Total
			case "locked":
				if resp.Locked != nil {
					value = resp.Locked.Reserve
				}
			default:
				http.Error(w, "type must be circulating, total, or locked", http.StatusBadRequest)
				return
			}
		default:
			http.Error(w, "asset must be rune or tcy", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "%d", value)
	}).Methods("GET")
}
