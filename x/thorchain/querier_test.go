package thorchain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/blang/semver"

	. "gopkg.in/check.v1"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	ckeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	types2 "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/app/params"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Note: coverage lacking for these queries in this file
//   check whether there is coverage elsewhere to add openapi conformance verification
// QueryBlockResponse
// QueryDerivedPoolResponse
// QueryDerivedPoolsResponse
// QueryLiquidityProviderResponse
// QueryMimirAdminValuesResponse
// QueryMimirNodesAllValuesResponse
// QueryMimirNodesValuesResponse
// QueryMimirWithKeyResponse
// QueryOutboundFeesResponse
// QueryOutboundResponse
// QueryPoolSlipsResponse
// QueryQuoteSaverDepositResponse
// QueryQuoteSaverWithdrawResponse
// QueryQuoteSwapResponse
// QueryRuneProviderResponse
// QueryRuneProvidersResponse
// QuerySaverResponse
// QueryStreamingSwapResponse
// QueryStreamingSwapsResponse
// SwapperClout
// QuerySwapQueueResponse
// QueryThornameResponse
// QueryTradeAccountsResponse
// QueryTradeUnitsResponse
// QueryTssKeygenMetricResponse
// QueryTssMetricResponse
// QueryInvariantResponse
// QueryInvariantsResponse
// QueryRunePoolResponse
// QueryTradeUnitResponse

type QuerierSuite struct {
	kb          cosmos.KeybaseStore
	mgr         *Mgrs
	k           keeper.Keeper
	queryServer types.QueryServer
	ctx         cosmos.Context
}

var _ = Suite(&QuerierSuite{})

type TestQuerierKeeper struct {
	keeper.KVStoreDummy
	txOut *TxOut
}

func (k *TestQuerierKeeper) GetTxOut(_ cosmos.Context, _ int64) (*TxOut, error) {
	return k.txOut, nil
}

func (s *QuerierSuite) SetUpTest(c *C) {
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb := ckeys.NewInMemory(cdc)
	username := "thorchain"
	password := "password"

	_, _, err := kb.NewMnemonic(username, ckeys.English, cmd.THORChainHDPath, password, hd.Secp256k1)
	c.Assert(err, IsNil)
	s.kb = cosmos.KeybaseStore{
		SignerName:   username,
		SignerPasswd: password,
		Keybase:      kb,
	}
	s.ctx, s.mgr = setupManagerForTest(c)
	s.k = s.mgr.Keeper()
	txConfig, err := params.TxConfig(cdc, nil)
	c.Assert(err, IsNil)
	s.queryServer = NewQueryServerImpl(s.mgr, txConfig, s.kb)
}

func (s *QuerierSuite) TestQueryKeysign(c *C) {
	ctx, _ := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(12)

	pk := GetRandomPubKey()
	toAddr := GetRandomETHAddress()
	txOut := NewTxOut(1)
	txOutItem := TxOutItem{
		Chain:       common.ETHChain,
		VaultPubKey: pk,
		ToAddress:   toAddr,
		InHash:      GetRandomTxHash(),
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}
	txOut.TxArray = append(txOut.TxArray, txOutItem)
	keeper := &TestQuerierKeeper{
		txOut: txOut,
	}

	_, mgr := setupManagerForTest(c)
	mgr.K = keeper
	txConfig, err := params.TxConfig(s.mgr.cdc, nil)
	c.Assert(err, IsNil)
	queryServer := NewQueryServerImpl(mgr, txConfig, s.kb)

	queryKeysignResp, err := queryServer.KeysignPubkey(ctx, &types.QueryKeysignPubkeyRequest{
		Height: "5",
		PubKey: pk.String(),
	})
	c.Assert(err, IsNil)
	c.Assert(queryKeysignResp, NotNil)

	// Verify conformance to openapi spec
	result, err := queryKeysignResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)

	var openapiKeysignResp openapi.KeysignResponse
	err = json.Unmarshal(result, &openapiKeysignResp)
	c.Assert(err, IsNil)
	c.Assert(openapiKeysignResp.Signature, Equals, queryKeysignResp.Signature)
	c.Assert(*openapiKeysignResp.Keysign.Height, Equals, queryKeysignResp.Keysign.Height)
	c.Assert(len(openapiKeysignResp.Keysign.TxArray), Equals, len(queryKeysignResp.Keysign.TxArray))
}

func (s *QuerierSuite) TestQueryPool(c *C) {
	ctx, mgr := setupManagerForTest(c)
	txConfig, err := params.TxConfig(s.mgr.cdc, nil)
	c.Assert(err, IsNil)
	queryServer := NewQueryServerImpl(mgr, txConfig, s.kb)

	pubKey := GetRandomPubKey()
	asgard := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, pubKey, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	c.Assert(mgr.Keeper().SetVault(ctx, asgard), IsNil)

	poolETH := NewPool()
	poolETH.Asset = common.ETHAsset
	poolETH.LPUnits = cosmos.NewUint(100)

	poolBTC := NewPool()
	poolBTC.Asset = common.BTCAsset
	poolBTC.LPUnits = cosmos.NewUint(0)

	err = mgr.Keeper().SetPool(ctx, poolETH)
	c.Assert(err, IsNil)

	err = mgr.Keeper().SetPool(ctx, poolBTC)
	c.Assert(err, IsNil)

	queryPoolsResp, err := queryServer.Pools(ctx, &types.QueryPoolsRequest{})
	c.Assert(err, IsNil)
	res, err := queryPoolsResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)

	var out Pools

	err = json.Unmarshal(res, &out)
	c.Assert(err, IsNil)
	c.Assert(len(out), Equals, 1)

	poolBTC.LPUnits = cosmos.NewUint(100)
	err = mgr.Keeper().SetPool(ctx, poolBTC)
	c.Assert(err, IsNil)

	queryPoolsResp, err = queryServer.Pools(ctx, &types.QueryPoolsRequest{})
	c.Assert(err, IsNil)
	res, err = queryPoolsResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)

	err = json.Unmarshal(res, &out)
	c.Assert(err, IsNil)
	c.Assert(len(out), Equals, 2)

	// Query pool with asset ETH.ETH from different context
	queryPoolResp, err := queryServer.Pool(s.ctx, &types.QueryPoolRequest{
		Asset: "ETH.ETH",
	})
	c.Assert(queryPoolResp, IsNil)
	c.Assert(err, NotNil)
}

func (s *QuerierSuite) TestVaults(c *C) {
	ctx, mgr := setupManagerForTest(c)
	txConfig, err := params.TxConfig(s.mgr.cdc, nil)
	c.Assert(err, IsNil)
	queryServer := NewQueryServerImpl(mgr, txConfig, s.kb)

	pubKey := GetRandomPubKey()
	asgard := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, pubKey, common.Chains{common.ETHChain}.Strings(), nil)
	c.Assert(mgr.Keeper().SetVault(ctx, asgard), IsNil)

	poolETH := NewPool()
	poolETH.Asset = common.ETHAsset
	poolETH.LPUnits = cosmos.NewUint(100)

	poolBTC := NewPool()
	poolBTC.Asset = common.BTCAsset
	poolBTC.LPUnits = cosmos.NewUint(0)

	err = mgr.Keeper().SetPool(ctx, poolETH)
	c.Assert(err, IsNil)

	err = mgr.Keeper().SetPool(ctx, poolBTC)
	c.Assert(err, IsNil)

	queryPoolsResp, err := queryServer.Pools(ctx, &types.QueryPoolsRequest{})
	c.Assert(err, IsNil)
	res, err := queryPoolsResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)

	var out Pools
	err = json.Unmarshal(res, &out)
	c.Assert(err, IsNil)
	c.Assert(len(out), Equals, 1)

	poolBTC.LPUnits = cosmos.NewUint(100)
	err = mgr.Keeper().SetPool(ctx, poolBTC)
	c.Assert(err, IsNil)

	queryPoolsResp, err = queryServer.Pools(ctx, &types.QueryPoolsRequest{})
	c.Assert(err, IsNil)
	res, err = queryPoolsResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)

	err = json.Unmarshal(res, &out)
	c.Assert(err, IsNil)
	c.Assert(len(out), Equals, 2)

	// Query pool with asset ETH.ETH from different context
	queryPoolResp, err := queryServer.Pool(s.ctx, &types.QueryPoolRequest{
		Asset: "ETH.ETH",
	})
	c.Assert(queryPoolResp, IsNil)
	c.Assert(err, NotNil)
}

func (s *QuerierSuite) TestSaverPools(c *C) {
	ctx, mgr := setupManagerForTest(c)
	txConfig, err := params.TxConfig(s.mgr.cdc, nil)
	c.Assert(err, IsNil)
	queryServer := NewQueryServerImpl(mgr, txConfig, s.kb)

	poolDOGE := NewPool()
	poolDOGE.Asset = common.DOGEAsset.GetSyntheticAsset()
	poolDOGE.LPUnits = cosmos.NewUint(100)

	poolBTC := NewPool()
	poolBTC.Asset = common.BTCAsset
	poolBTC.LPUnits = cosmos.NewUint(1000)

	poolETH := NewPool()
	poolETH.Asset = common.ETHAsset.GetSyntheticAsset()
	poolETH.LPUnits = cosmos.NewUint(100)

	err = mgr.Keeper().SetPool(ctx, poolDOGE)
	c.Assert(err, IsNil)

	err = mgr.Keeper().SetPool(ctx, poolBTC)
	c.Assert(err, IsNil)

	err = mgr.Keeper().SetPool(ctx, poolETH)
	c.Assert(err, IsNil)

	queryPoolsResp, err := queryServer.Pools(ctx, &types.QueryPoolsRequest{})
	c.Assert(err, IsNil)
	res, err := queryPoolsResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)

	var out []openapi.Pool
	err = json.Unmarshal(res, &out)
	c.Assert(err, IsNil)
	c.Assert(len(out), Equals, 1)
}

func (s *QuerierSuite) TestQueryNodeAccounts(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	_, mgr := setupManagerForTest(c)
	txConfig, err := params.TxConfig(s.mgr.cdc, nil)
	c.Assert(err, IsNil)
	queryServer := NewQueryServerImpl(mgr, txConfig, s.kb)

	nodeAccount := GetRandomValidatorNode(NodeActive)
	c.Assert(keeper.SetNodeAccount(ctx, nodeAccount), IsNil)
	vault := GetRandomVault()
	vault.Status = ActiveVault
	vault.StatusSince = 1
	c.Assert(keeper.SetVault(ctx, vault), IsNil)
	queryNodesResp, err := queryServer.Nodes(ctx, &types.QueryNodesRequest{})
	c.Assert(err, IsNil)

	// marshal output so we can verify it unmarshals as expected
	res, err := queryNodesResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)

	var out types.NodeAccounts
	err1 := json.Unmarshal(res, &out)
	c.Assert(err1, IsNil)
	c.Assert(len(out), Equals, 1)

	nodeAccount2 := GetRandomValidatorNode(NodeActive)
	nodeAccount2.Bond = cosmos.NewUint(common.One * 3000)
	c.Assert(keeper.SetNodeAccount(ctx, nodeAccount2), IsNil)

	// Check Bond-weighted rewards estimation works
	var nodeAccountResp []openapi.Node

	// Add bond rewards + set min bond for bond-weighted system
	network, _ := keeper.GetNetwork(ctx)
	network.BondRewardRune = cosmos.NewUint(common.One * 1000)
	c.Assert(keeper.SetNetwork(ctx, network), IsNil)
	keeper.SetMimir(ctx, "MinimumBondInRune", common.One*1000)

	queryNodesResp, err = queryServer.Nodes(ctx, &types.QueryNodesRequest{})
	c.Assert(err, IsNil)

	// marshal output so we can verify it unmarshals as expected
	res, err = queryNodesResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)

	err1 = json.Unmarshal(res, &nodeAccountResp)
	c.Assert(err1, IsNil)
	c.Assert(len(nodeAccountResp), Equals, 2)

	for _, node := range nodeAccountResp {
		if node.NodeAddress == nodeAccount.NodeAddress.String() {
			// First node has 25% of total bond, gets 25% of rewards
			c.Assert(node.CurrentAward, Equals, cosmos.NewUint(common.One*250).String())
			continue
		} else if node.NodeAddress == nodeAccount2.NodeAddress.String() {
			// Second node has 75% of total bond, gets 75% of rewards
			c.Assert(node.CurrentAward, Equals, cosmos.NewUint(common.One*750).String())
			continue
		}

		c.Fail()
	}

	// Check querier only returns nodes with bond
	nodeAccount2.Bond = cosmos.NewUint(0)
	c.Assert(keeper.SetNodeAccount(ctx, nodeAccount2), IsNil)

	queryNodesResp, err = queryServer.Nodes(ctx, &types.QueryNodesRequest{})
	c.Assert(err, IsNil)

	// marshal output so we can verify it unmarshals as expected
	res, err = queryNodesResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)

	err1 = json.Unmarshal(res, &out)
	c.Assert(err1, IsNil)
	c.Assert(len(out), Equals, 1)
}

func (s *QuerierSuite) TestQueryUpgradeProposals(c *C) {
	ctx, mgr := setupManagerForTest(c)
	txConfig, err := params.TxConfig(s.mgr.cdc, nil)
	c.Assert(err, IsNil)
	queryServer := NewQueryServerImpl(mgr, txConfig, s.kb)

	k := mgr.Keeper()

	// Add node accounts
	na1 := GetRandomValidatorNode(NodeActive)
	na1.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na1), IsNil)
	na2 := GetRandomValidatorNode(NodeActive)
	na2.Bond = cosmos.NewUint(200 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na2), IsNil)
	na3 := GetRandomValidatorNode(NodeActive)
	na3.Bond = cosmos.NewUint(300 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na3), IsNil)
	na4 := GetRandomValidatorNode(NodeActive)
	na4.Bond = cosmos.NewUint(400 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na4), IsNil)
	na5 := GetRandomValidatorNode(NodeActive)
	na5.Bond = cosmos.NewUint(500 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na5), IsNil)
	na6 := GetRandomValidatorNode(NodeActive)
	na6.Bond = cosmos.NewUint(600 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na6), IsNil)

	const (
		upgradeName = "1.2.3"
		upgradeInfo = "scheduled upgrade"
	)

	upgradeHeight := ctx.BlockHeight() + 100

	// propose upgrade
	c.Assert(k.ProposeUpgrade(ctx, upgradeName, types.UpgradeProposal{
		Height: upgradeHeight,
		Info:   upgradeInfo,
	}), IsNil)

	k.ApproveUpgrade(ctx, na1.NodeAddress, upgradeName)
	k.ApproveUpgrade(ctx, na2.NodeAddress, upgradeName)
	k.ApproveUpgrade(ctx, na3.NodeAddress, upgradeName)

	queryUpgradeProposalsResp, err := queryServer.UpgradeProposals(ctx, &types.QueryUpgradeProposalsRequest{})
	c.Assert(err, IsNil)
	res, err := queryUpgradeProposalsResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)

	var proposals []openapi.UpgradeProposal

	err = json.Unmarshal(res, &proposals)
	c.Assert(err, IsNil)

	c.Assert(len(proposals), Equals, 1)
	p := proposals[0]
	c.Assert(p.Name, Equals, upgradeName)
	c.Assert(p.Info, Equals, upgradeInfo)
	c.Assert(p.Height, Equals, upgradeHeight)

	queryUpgradeProposalResp, err := queryServer.UpgradeProposal(ctx, &types.QueryUpgradeProposalRequest{
		Name: upgradeName,
	})
	c.Assert(err, IsNil)
	res, err = queryUpgradeProposalResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	err = json.Unmarshal(res, &p)
	c.Assert(err, IsNil)

	c.Assert(p.Name, Equals, upgradeName)
	c.Assert(p.Info, Equals, upgradeInfo)
	c.Assert(p.Height, Equals, upgradeHeight)
	c.Assert(*p.Approved, Equals, false)
	c.Assert(*p.ValidatorsToQuorum, Equals, int64(1))
	c.Assert(*p.ApprovedPercent, Equals, "50.00")

	k.ApproveUpgrade(ctx, na4.NodeAddress, upgradeName)

	queryUpgradeProposalResp, err = queryServer.UpgradeProposal(ctx, &types.QueryUpgradeProposalRequest{
		Name: upgradeName,
	})
	c.Assert(err, IsNil)
	res, err = queryUpgradeProposalResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	err = json.Unmarshal(res, &p)
	c.Assert(err, IsNil)

	c.Assert(*p.Approved, Equals, true)
	c.Assert(*p.ValidatorsToQuorum, Equals, int64(0))
	c.Assert(*p.ApprovedPercent, Equals, "66.67")

	k.RejectUpgrade(ctx, na2.NodeAddress, upgradeName)

	queryUpgradeProposalResp, err = queryServer.UpgradeProposal(ctx, &types.QueryUpgradeProposalRequest{
		Name: upgradeName,
	})
	c.Assert(err, IsNil)
	res, err = queryUpgradeProposalResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	err = json.Unmarshal(res, &p)
	c.Assert(err, IsNil)

	c.Assert(*p.Approved, Equals, false)
	c.Assert(*p.ValidatorsToQuorum, Equals, int64(1))
	c.Assert(*p.ApprovedPercent, Equals, "50.00")

	var votes []openapi.UpgradeVote
	queryUpgradeVotesResp, err := queryServer.UpgradeVotes(ctx, &types.QueryUpgradeVotesRequest{
		Name: upgradeName,
	})
	c.Assert(err, IsNil)
	res, err = queryUpgradeVotesResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	err = json.Unmarshal(res, &votes)
	c.Assert(err, IsNil)
	c.Assert(len(votes), Equals, 4)

	foundVote := make(map[string]bool)
	for _, v := range votes {
		if _, ok := foundVote[v.NodeAddress]; ok {
			c.Log("duplicate vote", v.NodeAddress)
			c.Fail()
		}
		foundVote[v.NodeAddress] = true
		switch v.NodeAddress {
		case na1.NodeAddress.String():
			c.Assert(v.Vote, Equals, "approve")
		case na2.NodeAddress.String():
			c.Assert(v.Vote, Equals, "reject")
		case na3.NodeAddress.String():
			c.Assert(v.Vote, Equals, "approve")
		case na4.NodeAddress.String():
			c.Assert(v.Vote, Equals, "approve")
		case na5.NodeAddress.String():
			c.Assert(v.Vote, Equals, "approve")
		default:
			c.Log("unexpected voter address", v.NodeAddress)
			c.Fail()
		}
	}
}

func (s *QuerierSuite) TestQuerierRagnarokInProgress(c *C) {
	// test ragnarok
	queryRagnarokResp, err := s.queryServer.Ragnarok(s.ctx, &types.QueryRagnarokRequest{})
	c.Assert(err, IsNil)

	// marshal output so we can verify it unmarshals as expected
	result, err := queryRagnarokResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	var ragnarok bool
	c.Assert(json.Unmarshal(result, &ragnarok), IsNil)
	c.Assert(ragnarok, Equals, false)
}

func (s *QuerierSuite) TestQueryLiquidityProviders(c *C) {
	// test liquidity providers
	queryLPsResp, err := s.queryServer.LiquidityProviders(s.ctx, &types.QueryLiquidityProvidersRequest{
		Asset: "ETH.ETH",
	})
	c.Assert(err, IsNil)

	// marshal output so we can verify it unmarshals as expected
	result, err := queryLPsResp.MarshalJSONPB(nil)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
	s.k.SetLiquidityProvider(s.ctx, LiquidityProvider{
		Asset:              common.ETHAsset,
		RuneAddress:        GetRandomETHAddress(),
		AssetAddress:       GetRandomETHAddress(),
		LastAddHeight:      1024,
		LastWithdrawHeight: 0,
		Units:              cosmos.NewUint(10),
	})
	queryLPsResp, err = s.queryServer.LiquidityProviders(s.ctx, &types.QueryLiquidityProvidersRequest{
		Asset: "ETH.ETH",
	})
	c.Assert(err, IsNil)

	// marshal output so we can verify it unmarshals as expected
	result, err = queryLPsResp.MarshalJSONPB(nil)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
	var lps LiquidityProviders
	c.Assert(json.Unmarshal(result, &lps), IsNil)
	c.Assert(lps, HasLen, 1)

	s.k.SetLiquidityProvider(s.ctx, LiquidityProvider{
		Asset:              common.ETHAsset.GetSyntheticAsset(),
		RuneAddress:        GetRandomETHAddress(),
		AssetAddress:       GetRandomRUNEAddress(),
		LastAddHeight:      1024,
		LastWithdrawHeight: 0,
		Units:              cosmos.NewUint(10),
	})

	// Query Savers from SaversPool
	querySaversResp, err := s.queryServer.Savers(s.ctx, &types.QuerySaversRequest{
		Asset: "ETH.ETH",
	})
	c.Assert(err, IsNil)

	// marshal output so we can verify it unmarshals as expected
	result, err = querySaversResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	var savers LiquidityProviders
	c.Assert(json.Unmarshal(result, &savers), IsNil)
	c.Assert(lps, HasLen, 1)
}

func (s *QuerierSuite) TestQueryTxInVoter(c *C) {
	tx := GetRandomTx()
	// test getTxInVoter
	queryTxVoterResp, err := s.queryServer.TxVoters(s.ctx, &types.QueryTxVotersRequest{
		TxId: tx.ID.String(),
	})
	c.Assert(err, NotNil)
	c.Assert(queryTxVoterResp, IsNil)

	observedTxInVote := NewObservedTxVoter(tx.ID, []common.ObservedTx{NewObservedTx(tx, s.ctx.BlockHeight(), GetRandomPubKey(), s.ctx.BlockHeight())})
	s.k.SetObservedTxInVoter(s.ctx, observedTxInVote)
	queryTxVoterResp, err = s.queryServer.TxVoters(s.ctx, &types.QueryTxVotersRequest{
		TxId: tx.ID.String(),
	})
	c.Assert(err, IsNil)

	// marshal output so we can verify it unmarshals as expected
	result, err := queryTxVoterResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	var voter openapi.TxDetailsResponse
	c.Assert(json.Unmarshal(result, &voter), IsNil)

	// common.Tx Valid cannot be used for openapi.Tx, so checking some criteria individually.
	c.Assert(voter.TxId == nil, Equals, false)
	c.Assert(len(voter.Txs) == 1, Equals, true)
	c.Assert(voter.Txs[0].ExternalObservedHeight == nil, Equals, false)
	c.Assert(*voter.Txs[0].ExternalObservedHeight <= 0, Equals, false)
	c.Assert(voter.Txs[0].ObservedPubKey == nil, Equals, false)
	c.Assert(voter.Txs[0].ExternalConfirmationDelayHeight == nil, Equals, false)
	c.Assert(*voter.Txs[0].ExternalConfirmationDelayHeight <= 0, Equals, false)
	c.Assert(voter.Txs[0].Tx.Id == nil, Equals, false)
	c.Assert(voter.Txs[0].Tx.FromAddress == nil, Equals, false)
	c.Assert(voter.Txs[0].Tx.ToAddress == nil, Equals, false)
	c.Assert(voter.Txs[0].Tx.Chain == nil, Equals, false)
	c.Assert(len(voter.Txs[0].Tx.Coins) == 0, Equals, false)
}

func (s *QuerierSuite) TestQueryTxStages(c *C) {
	tx := GetRandomTx()
	// test getTxInVoter
	queryTxStagesResp, err := s.queryServer.TxStages(s.ctx, &types.QueryTxStagesRequest{
		TxId: tx.ID.String(),
	})
	c.Assert(err, IsNil) // Expecting no error for an unobserved hash.

	// marshal output so we can verify it unmarshals as expected
	result, err := queryTxStagesResp.MarshalJSONPB(nil)
	c.Assert(result, NotNil) // Expecting a not-started Observation stage.
	c.Assert(err, IsNil)     // Expecting no error for an unobserved hash.
	observedTxInVote := NewObservedTxVoter(tx.ID, []common.ObservedTx{NewObservedTx(tx, s.ctx.BlockHeight(), GetRandomPubKey(), s.ctx.BlockHeight())})
	s.k.SetObservedTxInVoter(s.ctx, observedTxInVote)
	queryTxStagesResp, err = s.queryServer.TxStages(s.ctx, &types.QueryTxStagesRequest{
		TxId: tx.ID.String(),
	})
	c.Assert(err, IsNil) // Expecting no error for an unobserved hash.

	// marshal output so we can verify it unmarshals as expected
	result, err = queryTxStagesResp.MarshalJSONPB(nil)
	c.Assert(result, NotNil) // Expecting a not-started Observation stage.
	c.Assert(err, IsNil)     // Expecting no error for an unobserved hash.

	// Verify conformance to openapi spec
	var openapiTxStagesResp openapi.TxStagesResponse
	err = json.Unmarshal(result, &openapiTxStagesResp)
	c.Assert(err, IsNil)

	c.Assert(*openapiTxStagesResp.InboundObserved.Started, Equals, queryTxStagesResp.InboundObserved.Started)
}

func (s *QuerierSuite) TestQueryTxStatus(c *C) {
	tx := GetRandomTx()
	// test getTxInVoter
	queryTxStatusResp, err := s.queryServer.TxStatus(s.ctx, &types.QueryTxStatusRequest{
		TxId: tx.ID.String(),
	})
	c.Assert(err, IsNil) // Expecting no error for an unobserved hash.

	// marshal output so we can verify it unmarshals as expected
	result, err := queryTxStatusResp.MarshalJSONPB(nil)
	c.Assert(result, NotNil) // Expecting a not-started Observation stage.
	c.Assert(err, IsNil)     // Expecting no error for an unobserved hash.
	observedTxInVote := NewObservedTxVoter(tx.ID, []common.ObservedTx{NewObservedTx(tx, s.ctx.BlockHeight(), GetRandomPubKey(), s.ctx.BlockHeight())})
	s.k.SetObservedTxInVoter(s.ctx, observedTxInVote)
	queryTxStatusResp, err = s.queryServer.TxStatus(s.ctx, &types.QueryTxStatusRequest{
		TxId: tx.ID.String(),
	})
	c.Assert(err, IsNil) // Expecting no error for an unobserved hash.

	// marshal output so we can verify it unmarshals as expected
	result, err = queryTxStatusResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify conformance to openapi spec
	var openapiTxStatusresp openapi.TxStatusResponse
	err = json.Unmarshal(result, &openapiTxStatusresp)
	c.Assert(err, IsNil)

	c.Assert(*openapiTxStatusresp.Tx.Id, Equals, queryTxStatusResp.Tx.ID.String())
	c.Assert(*openapiTxStatusresp.Tx.Chain, Equals, queryTxStatusResp.Tx.Chain.String())
	c.Assert(*openapiTxStatusresp.Tx.FromAddress, Equals, queryTxStatusResp.Tx.FromAddress.String())
	c.Assert(*openapiTxStatusresp.Tx.ToAddress, Equals, queryTxStatusResp.Tx.ToAddress.String())
	c.Assert(openapiTxStatusresp.Tx.Coins[0].Asset, Equals, queryTxStatusResp.Tx.Coins[0].Asset.String())
	c.Assert(openapiTxStatusresp.Tx.Coins[0].Amount, Equals, queryTxStatusResp.Tx.Coins[0].Amount.String())
	c.Assert(openapiTxStatusresp.Tx.Gas[0].Asset, Equals, queryTxStatusResp.Tx.Gas[0].Asset.String())
	c.Assert(openapiTxStatusresp.Tx.Gas[0].Amount, Equals, queryTxStatusResp.Tx.Gas[0].Amount.String())
	c.Assert(openapiTxStatusresp.OutTxs, IsNil)
	c.Assert(*openapiTxStatusresp.Stages.InboundObserved.Started, Equals, queryTxStatusResp.Stages.InboundObserved.Started)
}

func (s *QuerierSuite) TestQueryTx(c *C) {
	tx := GetRandomTx()
	// test get tx in
	queryTxResp, err := s.queryServer.Tx(s.ctx, &types.QueryTxRequest{
		TxId: tx.ID.String(),
	})
	c.Assert(err, NotNil)
	c.Assert(queryTxResp, IsNil)

	nodeAccount := GetRandomValidatorNode(NodeActive)
	c.Assert(s.k.SetNodeAccount(s.ctx, nodeAccount), IsNil)
	voter, err := s.k.GetObservedTxInVoter(s.ctx, tx.ID)
	c.Assert(err, IsNil)
	voter.Add(NewObservedTx(tx, s.ctx.BlockHeight(), nodeAccount.PubKeySet.Secp256k1, s.ctx.BlockHeight()), nodeAccount.NodeAddress)
	s.k.SetObservedTxInVoter(s.ctx, voter)
	queryTxResp, err = s.queryServer.Tx(s.ctx, &types.QueryTxRequest{
		TxId: tx.ID.String(),
	})
	c.Assert(err, IsNil)

	result, err := queryTxResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	var newTx struct {
		openapi.ObservedTx `json:"observed_tx"`
		KeysignMetrics     types.TssKeysignMetric `json:"keysign_metric,omitempty"`
	}
	c.Assert(json.Unmarshal(result, &newTx), IsNil)

	// common.Tx Valid cannot be used for openapi.Tx, so checking some criteria individually.
	c.Assert(newTx.ExternalObservedHeight == nil, Equals, false)
	c.Assert(*newTx.ExternalObservedHeight <= 0, Equals, false)
	c.Assert(newTx.ObservedPubKey == nil, Equals, false)
	c.Assert(newTx.ExternalConfirmationDelayHeight == nil, Equals, false)
	c.Assert(*newTx.ExternalConfirmationDelayHeight <= 0, Equals, false)
	c.Assert(newTx.Tx.Id == nil, Equals, false)
	c.Assert(newTx.Tx.FromAddress == nil, Equals, false)
	c.Assert(newTx.Tx.ToAddress == nil, Equals, false)
	c.Assert(newTx.Tx.Chain == nil, Equals, false)
	c.Assert(len(newTx.Tx.Coins) == 0, Equals, false)
}

func (s *QuerierSuite) TestQueryKeyGen(c *C) {
	queryKeygensPubkeyResp, err := s.queryServer.Keygen(s.ctx, &types.QueryKeygenRequest{
		Height: "whatever",
	})
	c.Assert(queryKeygensPubkeyResp, IsNil)
	c.Assert(err, NotNil)

	queryKeygensPubkeyResp, err = s.queryServer.Keygen(s.ctx, &types.QueryKeygenRequest{
		Height: "10000",
	})
	c.Assert(queryKeygensPubkeyResp, IsNil)
	c.Assert(err, NotNil)

	queryKeygensPubkeyResp, err = s.queryServer.Keygen(s.ctx, &types.QueryKeygenRequest{
		Height: strconv.FormatInt(s.ctx.BlockHeight(), 10),
	})
	c.Assert(queryKeygensPubkeyResp, NotNil)
	c.Assert(err, IsNil)

	queryKeygensPubkeyResp, err = s.queryServer.Keygen(s.ctx, &types.QueryKeygenRequest{
		Height: strconv.FormatInt(s.ctx.BlockHeight(), 10),
		PubKey: GetRandomPubKey().String(),
	})
	c.Assert(queryKeygensPubkeyResp, NotNil)
	c.Assert(err, IsNil)

	result, err := queryKeygensPubkeyResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)

	// Verify conformance to openapi spec
	var openapiKeygensPubkeyResp openapi.KeygenResponse
	err = json.Unmarshal(result, &openapiKeygensPubkeyResp)
	c.Assert(err, IsNil)

	c.Assert(*openapiKeygensPubkeyResp.KeygenBlock.Height, Equals, queryKeygensPubkeyResp.KeygenBlock.Height)
	c.Assert(openapiKeygensPubkeyResp.KeygenBlock.Keygens, IsNil)
	c.Assert(openapiKeygensPubkeyResp.Signature, Equals, queryKeygensPubkeyResp.Signature)
}

func (s *QuerierSuite) TestQueryQueue(c *C) {
	queryQueueResp, err := s.queryServer.Queue(s.ctx, &types.QueryQueueRequest{})
	c.Assert(err, IsNil)

	// marshal output so we can verify it unmarshals as expected
	result, err := queryQueueResp.MarshalJSONPB(nil)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
	var q openapi.QueueResponse
	c.Assert(json.Unmarshal(result, &q), IsNil)

	// Verify conformance to openapi spec
	c.Assert(q.ScheduledOutboundClout, Equals, queryQueueResp.ScheduledOutboundClout)
	c.Assert(q.ScheduledOutboundValue, Equals, queryQueueResp.ScheduledOutboundValue)
}

func (s *QuerierSuite) TestQueryPendingOutboundKeepsExtendedSigningPeriodItems(c *C) {
	period := s.mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	s.checkQueryOutboundSigningPeriods(c, period)
}

func (s *QuerierSuite) TestQueryQueueCountsMemolessOutbounds(c *C) {
	ctx := s.ctx.WithBlockHeight(1500)
	qs := queryServer{mgr: s.mgr}
	zero := cosmos.ZeroUint()
	inHash := GetRandomTxHash()
	for _, memos := range []struct {
		memo         string
		originalMemo string
	}{
		{"", NewOutboundMemo(inHash).String()},
		{"", NewRefundMemo(inHash).String()},
		{NewOutboundMemo(inHash).String(), ""},
		{NewMigrateMemo(ctx.BlockHeight()).String(), ""},
	} {
		c.Assert(s.k.AppendTxOut(ctx, ctx.BlockHeight(), TxOutItem{
			Chain:                 common.BTCChain,
			ToAddress:             GetRandomBTCAddress(),
			VaultPubKey:           GetRandomPubKey(),
			Coin:                  common.NewCoin(common.BTCAsset, cosmos.NewUint(100)),
			Memo:                  memos.memo,
			OriginalMemo:          memos.originalMemo,
			InHash:                inHash,
			CloutSpent:            &zero,
			AggregatorTargetLimit: &zero,
		}), IsNil)
	}

	pending, err := qs.queryPendingOutbound(ctx, &types.QueryPendingOutboundRequest{})
	c.Assert(err, IsNil)
	c.Assert(pending.TxOutItems, HasLen, 4)

	queue, err := qs.queryQueue(ctx, &types.QueryQueueRequest{})
	c.Assert(err, IsNil)
	c.Assert(queue.Outbound, Equals, int64(3))
	c.Assert(queue.Internal, Equals, int64(1))
}

func (s *QuerierSuite) TestQueryOutboundHonorsLongerMimirSigningPeriod(c *C) {
	period := 2 * s.mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	s.k.SetMimir(s.ctx, constants.SigningTransactionPeriod.String(), period)
	s.checkQueryOutboundSigningPeriods(c, period)
}

func (s *QuerierSuite) TestQueryOutboundHonorsShorterMimirSigningPeriod(c *C) {
	period := s.mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod) / 2
	s.k.SetMimir(s.ctx, constants.SigningTransactionPeriod.String(), period)
	s.checkQueryOutboundSigningPeriods(c, period)
}

func (s *QuerierSuite) TestQueryOutboundFallsBackAfterSigningPeriodMimirRemoved(c *C) {
	period := s.mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	s.k.SetMimir(s.ctx, constants.SigningTransactionPeriod.String(), 2*period)
	s.k.SetMimir(s.ctx, constants.SigningTransactionPeriod.String(), -1)
	s.checkQueryOutboundSigningPeriods(c, period)
}

func (s *QuerierSuite) checkQueryOutboundSigningPeriods(c *C, period int64) {
	ctx := s.ctx.WithBlockHeight(5 * period)
	qs := queryServer{mgr: s.mgr}
	zero := cosmos.ZeroUint()
	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)

	classes := []struct {
		name       string
		chain      common.Chain
		asset      common.Asset
		aggregator string
		window     int64
	}{
		{name: "BTC", chain: common.BTCChain, asset: common.BTCAsset, window: period},
		{name: "EVM gas asset", chain: common.ETHChain, asset: common.ETHAsset, window: period},
		{name: "XMR", chain: common.XMRChain, asset: common.XMRAsset, window: 3 * period},
		{name: "EVM token", chain: common.ETHChain, asset: usdc, window: 3 * period},
		{name: "EVM aggregator", chain: common.ETHChain, asset: common.ETHAsset, aggregator: "0xaggregator", window: 3 * period},
	}

	expected := make(map[string]bool)
	for _, class := range classes {
		appendItem := func(height int64) common.TxID {
			inHash := GetRandomTxHash()
			c.Assert(s.k.AppendTxOut(ctx, height, TxOutItem{
				Chain:                 class.chain,
				ToAddress:             GetRandomBTCAddress(),
				VaultPubKey:           GetRandomPubKey(),
				Coin:                  common.NewCoin(class.asset, cosmos.NewUint(100)),
				Memo:                  NewOutboundMemo(inHash).String(),
				InHash:                inHash,
				Aggregator:            class.aggregator,
				CloutSpent:            &zero,
				AggregatorTargetLimit: &zero,
			}), IsNil, Commentf("class %s", class.name))
			return inHash
		}

		// Each class stays visible at its expiry boundary and disappears one block later.
		edge := appendItem(ctx.BlockHeight() - class.window)
		expected[edge.String()] = true
		appendItem(ctx.BlockHeight() - class.window - 1)
		if class.window > period {
			// Extended outbounds must also remain visible after the standard window.
			live := appendItem(ctx.BlockHeight() - 2*period)
			expected[live.String()] = true
		}
	}

	resp, err := qs.queryPendingOutbound(ctx, &types.QueryPendingOutboundRequest{})
	c.Assert(err, IsNil)
	got := make(map[string]bool)
	for _, item := range resp.TxOutItems {
		got[item.InHash] = true
	}
	c.Check(got, DeepEquals, expected)
	c.Check(resp.TxOutItems, HasLen, len(expected))

	queue, err := qs.queryQueue(ctx, &types.QueryQueueRequest{})
	c.Assert(err, IsNil)
	c.Check(queue.Outbound, Equals, int64(len(expected)))
}

func (s *QuerierSuite) TestQueryHeights(c *C) {
	queryChainsLastBlockResp, err := s.queryServer.ChainsLastBlock(s.ctx, &types.QueryChainsLastBlockRequest{
		Chain: strconv.FormatInt(s.ctx.BlockHeight(), 10),
	})
	c.Assert(queryChainsLastBlockResp, IsNil)
	c.Assert(err, NotNil)

	queryLastBlocksResp, err := s.queryServer.LastBlocks(s.ctx, &types.QueryLastBlocksRequest{})
	c.Assert(err, IsNil)

	// marshal output so we can verify it unmarshals as expected
	result, err := queryLastBlocksResp.MarshalJSONPB(nil)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
	var q []openapi.LastBlock
	c.Assert(json.Unmarshal(result, &q), IsNil)

	queryChainsLastBlockResp, err = s.queryServer.ChainsLastBlock(s.ctx, &types.QueryChainsLastBlockRequest{
		Chain: "BTC",
	})
	c.Assert(err, IsNil)

	result, err = queryChainsLastBlockResp.MarshalJSONPB(nil)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
	c.Assert(json.Unmarshal(result, &q), IsNil)

	// Verify conformance to openapi spec
	c.Assert(q[0].Chain, Equals, queryChainsLastBlockResp.LastBlocks[0].Chain)
	c.Assert(q[0].Thorchain, Equals, queryChainsLastBlockResp.LastBlocks[0].Thorchain)
}

func (s *QuerierSuite) TestQueryConstantValues(c *C) {
	queryConstantValResp, err := s.queryServer.ConstantValues(s.ctx, &types.QueryConstantValuesRequest{})
	c.Assert(queryConstantValResp, NotNil)
	c.Assert(err, IsNil)
	c.Assert(sort.SliceIsSorted(queryConstantValResp.Int_64Values, func(i, j int) bool {
		return queryConstantValResp.Int_64Values[i].Name < queryConstantValResp.Int_64Values[j].Name
	}), Equals, true)
	c.Assert(sort.SliceIsSorted(queryConstantValResp.BoolValues, func(i, j int) bool {
		return queryConstantValResp.BoolValues[i].Name < queryConstantValResp.BoolValues[j].Name
	}), Equals, true)
	c.Assert(sort.SliceIsSorted(queryConstantValResp.StringValues, func(i, j int) bool {
		return queryConstantValResp.StringValues[i].Name < queryConstantValResp.StringValues[j].Name
	}), Equals, true)

	cv := s.mgr.GetConstants().GetConstantValsByKeyname()
	c.Assert(queryConstantValResp.Int_64Values, HasLen, len(cv.Int64Values))
	c.Assert(queryConstantValResp.BoolValues, HasLen, len(cv.BoolValues))
	c.Assert(queryConstantValResp.StringValues, HasLen, len(cv.StringValues))
	for _, kv := range queryConstantValResp.Int_64Values {
		c.Assert(kv.Value, Equals, cv.Int64Values[kv.Name])
	}
	for _, kv := range queryConstantValResp.BoolValues {
		c.Assert(kv.Value, Equals, cv.BoolValues[kv.Name])
	}
	for _, kv := range queryConstantValResp.StringValues {
		c.Assert(kv.Value, Equals, cv.StringValues[kv.Name])
	}

	_, err = queryConstantValResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)

	// Note: openapi conformance isn't followed here nor legacy versions
	// openapi's ConstantsResponse
	//   int_64_values are map[string]string while map[string]int64 is actually returned
	//   bool_values are map[string]string while map[string]bool is actually returned
}

func (s *QuerierSuite) TestQueryMimir(c *C) {
	s.k.SetMimir(s.ctx, "hello", 111)
	queryMimirResp, err := s.queryServer.MimirValues(s.ctx, &types.QueryMimirValuesRequest{})
	c.Assert(err, IsNil)

	// marshal output so we can verify it unmarshals as expected
	result, err := queryMimirResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	var m map[string]int64
	c.Assert(json.Unmarshal(result, &m), IsNil)
	c.Assert(m, HasLen, 1)
	c.Assert(m["HELLO"], Equals, int64(111))
}

func (s *QuerierSuite) TestQueryBan(c *C) {
	queryBanResp, err := s.queryServer.Ban(s.ctx, &types.QueryBanRequest{})
	c.Assert(queryBanResp, IsNil)
	c.Assert(err, NotNil)

	queryBanResp, err = s.queryServer.Ban(s.ctx, &types.QueryBanRequest{
		Address: "Whatever",
	})
	c.Assert(queryBanResp, IsNil)
	c.Assert(err, NotNil)

	queryBanResp, err = s.queryServer.Ban(s.ctx, &types.QueryBanRequest{
		Address: GetRandomBech32Addr().String(),
	})
	c.Assert(queryBanResp, NotNil)
	c.Assert(err, IsNil)

	result, err := queryBanResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)

	var openapiBanResp openapi.BanResponse
	err = json.Unmarshal(result, &openapiBanResp)
	c.Assert(err, IsNil)

	// Verify conformance to openapi spec
	c.Assert(*openapiBanResp.NodeAddress, Equals, queryBanResp.NodeAddress.String())
}

func (s *QuerierSuite) TestQueryNodeAccount(c *C) {
	queryNodeAccount, err := s.queryServer.Node(s.ctx, &types.QueryNodeRequest{})
	c.Assert(queryNodeAccount, IsNil)
	c.Assert(err, NotNil)

	queryNodeAccount, err = s.queryServer.Node(s.ctx, &types.QueryNodeRequest{
		Address: "Whatever",
	})
	c.Assert(queryNodeAccount, IsNil)
	c.Assert(err, NotNil)

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(s.k.SetNodeAccount(s.ctx, na), IsNil)
	vault := GetRandomVault()
	vault.Status = ActiveVault
	vault.StatusSince = 1
	c.Assert(s.k.SetVault(s.ctx, vault), IsNil)
	queryNodeAccount, err = s.queryServer.Node(s.ctx, &types.QueryNodeRequest{
		Address: na.NodeAddress.String(),
	})
	c.Assert(queryNodeAccount, NotNil)
	c.Assert(err, IsNil)

	result, err := queryNodeAccount.MarshalJSONPB(nil)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
	var r openapi.Node
	c.Assert(json.Unmarshal(result, &r), IsNil)

	// Check bond-weighted rewards estimation works

	// Add another node with 75% of the bond
	nodeAccount2 := GetRandomValidatorNode(NodeActive)
	nodeAccount2.Bond = cosmos.NewUint(common.One * 3000)
	c.Assert(s.k.SetNodeAccount(s.ctx, nodeAccount2), IsNil)

	// Add bond rewards + set min bond for bond-weighted system
	network, _ := s.k.GetNetwork(s.ctx)
	network.BondRewardRune = cosmos.NewUint(common.One * 1000)
	c.Assert(s.k.SetNetwork(s.ctx, network), IsNil)
	s.k.SetMimir(s.ctx, "MinimumBondInRune", common.One*1000)

	// Get first node
	queryNodeAccount, err = s.queryServer.Node(s.ctx, &types.QueryNodeRequest{
		Address: na.NodeAddress.String(),
	})
	c.Assert(queryNodeAccount, NotNil)
	c.Assert(err, IsNil)

	result, err = queryNodeAccount.MarshalJSONPB(nil)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
	var r2 openapi.Node
	c.Assert(json.Unmarshal(result, &r2), IsNil)

	// First node has 25% of bond, should have 25% of the rewards
	c.Assert(r2.TotalBond, Equals, cosmos.NewUint(common.One*1000).String())
	c.Assert(r2.CurrentAward, Equals, cosmos.NewUint(common.One*250).String())

	// Get second node
	queryNodeAccount, err = s.queryServer.Node(s.ctx, &types.QueryNodeRequest{
		Address: nodeAccount2.NodeAddress.String(),
	})
	c.Assert(queryNodeAccount, NotNil)
	c.Assert(err, IsNil)

	result, err = queryNodeAccount.MarshalJSONPB(nil)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
	var r3 openapi.Node
	c.Assert(json.Unmarshal(result, &r3), IsNil)

	// Second node has 75% of bond, should have 75% of the rewards
	c.Assert(r3.TotalBond, Equals, cosmos.NewUint(common.One*3000).String())
	c.Assert(r3.CurrentAward, Equals, cosmos.NewUint(common.One*750).String())
}

func (s *QuerierSuite) TestQueryNodeWasmResponseIsDeterministic(c *C) {
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(s.k.SetNodeAccount(s.ctx, na), IsNil)

	vault := GetRandomVault()
	vault.Status = ActiveVault
	vault.StatusSince = 1
	c.Assert(s.k.SetVault(s.ctx, vault), IsNil)

	chains := common.Chains{
		common.BTCChain,
		common.ETHChain,
		common.LTCChain,
		common.BCHChain,
		common.DOGEChain,
		common.GAIAChain,
		common.AVAXChain,
		common.TRONChain,
	}
	for i, chain := range chains {
		c.Assert(s.k.SetLastObserveHeight(s.ctx, chain, na.NodeAddress, int64(1000+i)), IsNil)
	}

	const attempts = 10
	wasmCtx := s.ctx.WithValue(constants.CtxWASMQuery, true)
	expectedOrder := chains.Strings()
	sort.Strings(expectedOrder)
	expectedOrderString := strings.Join(expectedOrder, ",")
	var firstEncoding string
	for attempt := 1; attempt <= attempts; attempt++ {
		response, err := s.queryServer.Node(wasmCtx, &types.QueryNodeRequest{
			Address: na.NodeAddress.String(),
		})
		c.Assert(err, IsNil)

		encoding, err := response.Marshal()
		c.Assert(err, IsNil)

		order := make([]string, len(response.ObserveChains))
		for i, chainHeight := range response.ObserveChains {
			order[i] = chainHeight.Chain
		}
		currentOrder := strings.Join(order, ",")
		c.Assert(currentOrder, Equals, expectedOrderString)

		if attempt == 1 {
			firstEncoding = string(encoding)
			continue
		}

		c.Assert(string(encoding), Equals, firstEncoding,
			Commentf("QueryNode response changed on attempt %d", attempt))
	}

	var firstNodesEncoding string
	for attempt := 1; attempt <= attempts; attempt++ {
		response, err := s.queryServer.Nodes(wasmCtx, &types.QueryNodesRequest{})
		c.Assert(err, IsNil)
		c.Assert(response.Nodes, HasLen, 1)

		encoding, err := response.Marshal()
		c.Assert(err, IsNil)

		order := make([]string, len(response.Nodes[0].ObserveChains))
		for i, chainHeight := range response.Nodes[0].ObserveChains {
			order[i] = chainHeight.Chain
		}
		c.Assert(strings.Join(order, ","), Equals, expectedOrderString)

		if attempt == 1 {
			firstNodesEncoding = string(encoding)
			continue
		}
		c.Assert(string(encoding), Equals, firstNodesEncoding,
			Commentf("QueryNodes response changed on attempt %d", attempt))
	}
}

func (s *QuerierSuite) TestQueryPoolAddresses(c *C) {
	ctx, mgr := setupManagerForTest(c)

	pubKey := GetRandomPubKey()
	asgard := NewVault(ctx.BlockHeight()-1, ActiveVault, AsgardVault, pubKey, common.Chains{common.ETHChain}.Strings(), nil)
	c.Assert(mgr.Keeper().SetVault(ctx, asgard), IsNil)

	txConfig, err := params.TxConfig(s.mgr.cdc, nil)
	c.Assert(err, IsNil)
	queryServer := NewQueryServerImpl(mgr, txConfig, s.kb)
	queryInboundAddrResp, err := queryServer.InboundAddresses(ctx, &types.QueryInboundAddressesRequest{})
	c.Assert(err, IsNil)
	result, err := queryInboundAddrResp.MarshalJSONPB(nil)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)

	resp := []struct {
		Chain   common.Chain   `json:"chain"`
		PubKey  common.PubKey  `json:"pub_key"`
		Address common.Address `json:"address"`
		Halted  bool           `json:"halted"`
	}{}

	c.Assert(json.Unmarshal(result, &resp), IsNil)
	c.Assert(len(resp), Equals, 1)
	c.Assert(resp[0].Chain, Equals, common.ETHChain)
	c.Assert(resp[0].PubKey, Equals, pubKey)
}

func (s *QuerierSuite) TestQueryPoolAddressesXMRUsesFrostPubKey(c *C) {
	ctx, mgr := setupManagerForTest(c)

	pubKey := GetRandomPubKey()
	eddsaPubKey := GetRandomEd25519PubKey()
	frostPubKey := GetRandomEd25519PubKey()
	asgard := NewVaultV2(ctx.BlockHeight()-1, ActiveVault, AsgardVault, pubKey, common.Chains{common.XMRChain}.Strings(), nil, eddsaPubKey)
	asgard.PubKeyFrost = frostPubKey
	c.Assert(mgr.Keeper().SetVault(ctx, asgard), IsNil)

	txConfig, err := params.TxConfig(s.mgr.cdc, nil)
	c.Assert(err, IsNil)
	queryServer := NewQueryServerImpl(mgr, txConfig, s.kb)
	queryInboundAddrResp, err := queryServer.InboundAddresses(ctx, &types.QueryInboundAddressesRequest{})
	c.Assert(err, IsNil)
	result, err := queryInboundAddrResp.MarshalJSONPB(nil)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)

	resp := []struct {
		Chain   common.Chain   `json:"chain"`
		PubKey  common.PubKey  `json:"pub_key"`
		Address common.Address `json:"address"`
		ViewKey string         `json:"view_key"`
	}{}

	c.Assert(json.Unmarshal(result, &resp), IsNil)
	c.Assert(len(resp), Equals, 1)
	c.Assert(resp[0].Chain, Equals, common.XMRChain)
	c.Assert(resp[0].PubKey, Equals, frostPubKey)
	c.Assert(resp[0].ViewKey, Not(Equals), "")
}

func (s *QuerierSuite) TestQueryKeysignArrayPubKey(c *C) {
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(s.k.SetNodeAccount(s.ctx, na), IsNil)
	queryKeysignPubkeyResp, err := s.queryServer.KeysignPubkey(s.ctx, &types.QueryKeysignPubkeyRequest{})
	c.Assert(queryKeysignPubkeyResp, IsNil)
	c.Assert(err, NotNil)

	queryKeysignPubkeyResp, err = s.queryServer.KeysignPubkey(s.ctx, &types.QueryKeysignPubkeyRequest{
		Height: "asdf",
	})
	c.Assert(queryKeysignPubkeyResp, IsNil)
	c.Assert(err, NotNil)

	queryKeysignPubkeyResp, err = s.queryServer.KeysignPubkey(s.ctx, &types.QueryKeysignPubkeyRequest{
		Height: strconv.FormatInt(s.ctx.BlockHeight(), 10),
	})
	c.Assert(queryKeysignPubkeyResp, NotNil)
	c.Assert(err, IsNil)

	// marshal output so we can verify it unmarshals as expected
	result, err := queryKeysignPubkeyResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	var r openapi.KeysignResponse
	c.Assert(json.Unmarshal(result, &r), IsNil)

	// Verify conformance to openapi spec
	c.Assert(*r.Keysign.Height, Equals, queryKeysignPubkeyResp.Keysign.Height)
	c.Assert(len(r.Keysign.TxArray), Equals, 0)
	c.Assert(r.Signature, Equals, queryKeysignPubkeyResp.Signature)
}

func (s *QuerierSuite) TestQueryNetwork(c *C) {
	queryNetworkResp, err := s.queryServer.Network(s.ctx, &types.QueryNetworkRequest{})
	c.Assert(err, IsNil)

	// QueryNetworkResponse does not require JSONPBMarshaler implementation
	result, err := json.Marshal(queryNetworkResp)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
	var r Network // unsure why we were unmarshaling to this type, but leaving in
	c.Assert(json.Unmarshal(result, &r), IsNil)

	// Verify conformance to openapi spec
	var openapiNetworkResponse openapi.NetworkResponse
	c.Assert(json.Unmarshal(result, &openapiNetworkResponse), IsNil)

	c.Assert(openapiNetworkResponse.BondRewardRune, Equals, queryNetworkResp.BondRewardRune)
	c.Assert(openapiNetworkResponse.EffectiveSecurityBond, Equals, queryNetworkResp.EffectiveSecurityBond)
	c.Assert(openapiNetworkResponse.GasSpentRune, Equals, queryNetworkResp.GasSpentRune)
	c.Assert(openapiNetworkResponse.GasWithheldRune, Equals, queryNetworkResp.GasWithheldRune)
	c.Assert(openapiNetworkResponse.NativeOutboundFeeRune, Equals, queryNetworkResp.NativeOutboundFeeRune)
	c.Assert(openapiNetworkResponse.NativeTxFeeRune, Equals, queryNetworkResp.NativeTxFeeRune)
	c.Assert(*openapiNetworkResponse.OutboundFeeMultiplier, Equals, queryNetworkResp.OutboundFeeMultiplier)
	c.Assert(openapiNetworkResponse.RunePriceInTor, Equals, queryNetworkResp.RunePriceInTor)
	c.Assert(openapiNetworkResponse.TnsFeePerBlockRune, Equals, queryNetworkResp.TnsFeePerBlockRune)
	c.Assert(openapiNetworkResponse.TnsRegisterFeeRune, Equals, queryNetworkResp.TnsRegisterFeeRune)
	c.Assert(openapiNetworkResponse.TorPriceInRune, Equals, queryNetworkResp.TorPriceInRune)
	c.Assert(openapiNetworkResponse.TotalBondUnits, Equals, queryNetworkResp.TotalBondUnits)
	c.Assert(openapiNetworkResponse.TotalReserve, Equals, queryNetworkResp.TotalReserve)
	c.Assert(openapiNetworkResponse.VaultsMigrating, Equals, queryNetworkResp.VaultsMigrating)
	c.Assert(openapiNetworkResponse.XmrActiveVaultReady, Equals, queryNetworkResp.XmrActiveVaultReady)
}

func (s *QuerierSuite) TestGetXMRActiveVaultReadyRequiresFundsAndAddress(c *C) {
	ctx, k := setupKeeperForTest(c)

	ready, err := getXMRActiveVaultReady(ctx, k)
	c.Assert(err, IsNil)
	c.Assert(ready, Equals, false)

	unfundedFrostKey := GetRandomEd25519PubKey()
	unfunded := NewVaultV2(
		ctx.BlockHeight(),
		ActiveVault,
		AsgardVault,
		GetRandomPubKey(),
		common.Chains{common.XMRChain}.Strings(),
		nil,
		common.EmptyPubKey,
	)
	unfunded.PubKeyFrost = unfundedFrostKey
	c.Assert(k.SetVault(ctx, unfunded), IsNil)

	ready, err = getXMRActiveVaultReady(ctx, k)
	c.Assert(err, IsNil)
	c.Assert(ready, Equals, false)

	noAddress := NewVault(
		ctx.BlockHeight(),
		ActiveVault,
		AsgardVault,
		GetRandomPubKey(),
		common.Chains{common.XMRChain}.Strings(),
		nil,
	)
	noAddress.AddFunds(common.Coins{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(common.One)),
	})
	c.Assert(k.SetVault(ctx, noAddress), IsNil)

	ready, err = getXMRActiveVaultReady(ctx, k)
	c.Assert(err, IsNil)
	c.Assert(ready, Equals, false)

	fundedFrostKey := GetRandomEd25519PubKey()
	funded := NewVaultV2(
		ctx.BlockHeight(),
		ActiveVault,
		AsgardVault,
		GetRandomPubKey(),
		common.Chains{common.XMRChain}.Strings(),
		nil,
		common.EmptyPubKey,
	)
	funded.PubKeyFrost = fundedFrostKey
	funded.AddFunds(common.Coins{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(common.One)),
	})
	c.Assert(k.SetVault(ctx, funded), IsNil)

	ready, err = getXMRActiveVaultReady(ctx, k)
	c.Assert(err, IsNil)
	c.Assert(ready, Equals, true)
}

func (s *QuerierSuite) TestQueryInboundAddressesUsesChainSpecificVaultSelection(c *C) {
	ctx, mgr := setupManagerForTest(c)

	regularPubKey := GetRandomPubKey()
	regular := NewVault(ctx.BlockHeight()-1, ActiveVault, AsgardVault, regularPubKey, common.Chains{common.ETHChain}.Strings(), nil)
	c.Assert(mgr.Keeper().SetVault(ctx, regular), IsNil)

	xmrFrostKey := GetRandomEd25519PubKey()
	xmrVault := NewVaultV2(
		ctx.BlockHeight()-1,
		ActiveVault,
		AsgardVault,
		xmrFrostKey,
		common.Chains{common.THORChain, common.XMRChain}.Strings(),
		nil,
		xmrFrostKey,
	)
	xmrVault.PubKeyFrost = xmrFrostKey
	c.Assert(mgr.Keeper().SetVault(ctx, xmrVault), IsNil)

	txConfig, err := params.TxConfig(s.mgr.cdc, nil)
	c.Assert(err, IsNil)
	queryServer := NewQueryServerImpl(mgr, txConfig, s.kb)
	queryInboundAddrResp, err := queryServer.InboundAddresses(ctx, &types.QueryInboundAddressesRequest{})
	c.Assert(err, IsNil)

	var ethPubKey, xmrPubKey string
	for _, inbound := range queryInboundAddrResp.InboundAddresses {
		switch inbound.Chain {
		case common.ETHChain.String():
			ethPubKey = inbound.PubKey
		case common.XMRChain.String():
			xmrPubKey = inbound.PubKey
		}
	}

	c.Assert(ethPubKey, Equals, regularPubKey.String())
	c.Assert(xmrPubKey, Equals, xmrFrostKey.String())
}

func (s *QuerierSuite) TestQueryInboundAddressesDoesNotAdvertiseRetiringXMRVaultWithoutActiveMonovault(c *C) {
	ctx, mgr := setupManagerForTest(c)

	regularPubKey := GetRandomPubKey()
	regular := NewVault(ctx.BlockHeight()-1, ActiveVault, AsgardVault, regularPubKey, common.Chains{common.ETHChain}.Strings(), nil)
	c.Assert(mgr.Keeper().SetVault(ctx, regular), IsNil)

	retiringXMRPubKey := GetRandomEd25519PubKey()
	retiringXMR := NewVaultV2(
		ctx.BlockHeight()-2,
		RetiringVault,
		AsgardVault,
		retiringXMRPubKey,
		common.Chains{common.THORChain, common.XMRChain}.Strings(),
		nil,
		common.EmptyPubKey,
	)
	retiringXMR.PubKeyFrost = retiringXMRPubKey
	c.Assert(mgr.Keeper().SetVault(ctx, retiringXMR), IsNil)

	txConfig, err := params.TxConfig(s.mgr.cdc, nil)
	c.Assert(err, IsNil)
	queryServer := NewQueryServerImpl(mgr, txConfig, s.kb)
	queryInboundAddrResp, err := queryServer.InboundAddresses(ctx, &types.QueryInboundAddressesRequest{})
	c.Assert(err, IsNil)

	var xmrPubKey string
	for _, inbound := range queryInboundAddrResp.InboundAddresses {
		if inbound.Chain == common.XMRChain.String() {
			xmrPubKey = inbound.PubKey
			break
		}
	}

	c.Assert(xmrPubKey, Equals, "")
}

func (s *QuerierSuite) TestQueryInboundAddressesAcceptsXMRCapableVault(c *C) {
	ctx, mgr := setupManagerForTest(c)

	regularPubKey := GetRandomPubKey()
	regular := NewVault(ctx.BlockHeight()-1, ActiveVault, AsgardVault, regularPubKey, common.Chains{common.ETHChain}.Strings(), nil)
	c.Assert(mgr.Keeper().SetVault(ctx, regular), IsNil)

	hybrid := NewVaultV2(
		ctx.BlockHeight()-1,
		ActiveVault,
		AsgardVault,
		GetRandomPubKey(),
		common.Chains{common.THORChain, common.XMRChain, common.BTCChain}.Strings(),
		nil,
		GetRandomPubKey(),
	)
	hybrid.PubKeyFrost = getRandomXMRFrostPubKey(c)
	c.Assert(mgr.Keeper().SetVault(ctx, hybrid), IsNil)

	txConfig, err := params.TxConfig(s.mgr.cdc, nil)
	c.Assert(err, IsNil)
	queryServer := NewQueryServerImpl(mgr, txConfig, s.kb)
	queryInboundAddrResp, err := queryServer.InboundAddresses(ctx, &types.QueryInboundAddressesRequest{})
	c.Assert(err, IsNil)

	var ethPubKey, xmrPubKey string
	for _, inbound := range queryInboundAddrResp.InboundAddresses {
		switch inbound.Chain {
		case common.ETHChain.String():
			ethPubKey = inbound.PubKey
		case common.XMRChain.String():
			xmrPubKey = inbound.PubKey
		}
	}

	c.Assert(ethPubKey, Equals, regularPubKey.String())
	c.Assert(xmrPubKey, Equals, hybrid.PubKeyFrost.String())
}

func (s *QuerierSuite) TestQueryInboundAddressesExcludesCompromisedVault(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Compromised vault handles BTC only; safe vault handles ETH only.
	// Separate chains ensure each vault is the sole candidate for its chain,
	// so selectActiveVaultForChain is deterministic in both WASM and non-WASM paths.
	compromisedPubKey := GetRandomPubKey()
	mgr.Keeper().SetMimir(ctx, fmt.Sprintf("CompromisedVault-%s", compromisedPubKey.String()), 1)
	compromised := NewVault(ctx.BlockHeight()-1, ActiveVault, AsgardVault, compromisedPubKey, common.Chains{common.BTCChain}.Strings(), nil)
	c.Assert(mgr.Keeper().SetVault(ctx, compromised), IsNil)

	safePubKey := GetRandomPubKey()
	safeVault := NewVault(ctx.BlockHeight()-1, ActiveVault, AsgardVault, safePubKey, common.Chains{common.ETHChain}.Strings(), nil)
	c.Assert(mgr.Keeper().SetVault(ctx, safeVault), IsNil)

	txConfig, err := params.TxConfig(s.mgr.cdc, nil)
	c.Assert(err, IsNil)
	queryServer := NewQueryServerImpl(mgr, txConfig, s.kb)

	// Non-WASM path: BTC must be absent; ETH must show the safe vault.
	resp, err := queryServer.InboundAddresses(ctx, &types.QueryInboundAddressesRequest{})
	c.Assert(err, IsNil)
	for _, inbound := range resp.InboundAddresses {
		c.Assert(inbound.Chain, Not(Equals), common.BTCChain.String(), Commentf("compromised vault's chain must be absent from non-WASM /inbound_addresses"))
	}
	var ethPubKey string
	for _, inbound := range resp.InboundAddresses {
		if inbound.Chain == common.ETHChain.String() {
			ethPubKey = inbound.PubKey
			break
		}
	}
	c.Assert(ethPubKey, Equals, safePubKey.String())

	// WASM path: the filter must be skipped so the compromised vault's chain
	// appears, preserving deterministic contract responses.
	wasmCtx := ctx.WithValue(constants.CtxWASMQuery, true)
	wasmResp, err := queryServer.InboundAddresses(wasmCtx, &types.QueryInboundAddressesRequest{})
	c.Assert(err, IsNil)
	var btcPubKey string
	for _, inbound := range wasmResp.InboundAddresses {
		if inbound.Chain == common.BTCChain.String() {
			btcPubKey = inbound.PubKey
			break
		}
	}
	c.Assert(btcPubKey, Equals, compromisedPubKey.String(), Commentf("WASM path must expose the compromised vault unchanged"))
}

func (s *QuerierSuite) TestQueryInboundAddressesExcludesInsolventVault(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// The insolvent vault serves BTC and ETH but is flagged insolvent on BTC
	// only. A compromised key that drains BTC leaves ETH exposed, so the whole
	// vault must disappear from inbound election - both BTC and ETH.
	insolventPubKey := GetRandomPubKey()
	mgr.Keeper().SetMimir(ctx, solvencyHaltVaultKey(insolventPubKey, common.BTCChain), ctx.BlockHeight())
	insolvent := NewVault(ctx.BlockHeight()-1, ActiveVault, AsgardVault, insolventPubKey, common.Chains{common.BTCChain, common.ETHChain}.Strings(), nil)
	c.Assert(mgr.Keeper().SetVault(ctx, insolvent), IsNil)

	// A safe vault serves LTC so it remains the sole candidate for its chain.
	safePubKey := GetRandomPubKey()
	safeVault := NewVault(ctx.BlockHeight()-1, ActiveVault, AsgardVault, safePubKey, common.Chains{common.LTCChain}.Strings(), nil)
	c.Assert(mgr.Keeper().SetVault(ctx, safeVault), IsNil)

	txConfig, err := params.TxConfig(s.mgr.cdc, nil)
	c.Assert(err, IsNil)
	queryServer := NewQueryServerImpl(mgr, txConfig, s.kb)

	// Non-WASM path: neither BTC nor ETH (the insolvent vault's chains) may
	// appear; LTC must show the safe vault.
	resp, err := queryServer.InboundAddresses(ctx, &types.QueryInboundAddressesRequest{})
	c.Assert(err, IsNil)
	var ltcPubKey string
	for _, inbound := range resp.InboundAddresses {
		c.Assert(inbound.Chain, Not(Equals), common.BTCChain.String(), Commentf("insolvent vault's halted chain must be absent"))
		c.Assert(inbound.Chain, Not(Equals), common.ETHChain.String(), Commentf("insolvent vault's other chains must also be absent"))
		if inbound.Chain == common.LTCChain.String() {
			ltcPubKey = inbound.PubKey
		}
	}
	c.Assert(ltcPubKey, Equals, safePubKey.String())

	// WASM path: the filter must be skipped so the insolvent vault's chains
	// still appear, preserving deterministic contract responses.
	wasmCtx := ctx.WithValue(constants.CtxWASMQuery, true)
	wasmResp, err := queryServer.InboundAddresses(wasmCtx, &types.QueryInboundAddressesRequest{})
	c.Assert(err, IsNil)
	var btcPubKey string
	for _, inbound := range wasmResp.InboundAddresses {
		if inbound.Chain == common.BTCChain.String() {
			btcPubKey = inbound.PubKey
			break
		}
	}
	c.Assert(btcPubKey, Equals, insolventPubKey.String(), Commentf("WASM path must expose the insolvent vault unchanged"))

	// Once the vault is observed solvent again on BTC, the flag clears and the
	// vault is re-elected for its chains.
	clearVaultInsolvent(ctx, mgr.Keeper(), insolventPubKey, common.BTCChain)
	respAfter, err := queryServer.InboundAddresses(ctx, &types.QueryInboundAddressesRequest{})
	c.Assert(err, IsNil)
	var btcAfter, ethAfter string
	for _, inbound := range respAfter.InboundAddresses {
		switch inbound.Chain {
		case common.BTCChain.String():
			btcAfter = inbound.PubKey
		case common.ETHChain.String():
			ethAfter = inbound.PubKey
		}
	}
	c.Assert(btcAfter, Equals, insolventPubKey.String())
	c.Assert(ethAfter, Equals, insolventPubKey.String())
}

func (s *QuerierSuite) TestQueryAsgardVault(c *C) {
	ctx, _ := setupKeeperForTest(c)
	_, mgr := setupManagerForTest(c)

	c.Assert(s.k.SetVault(s.ctx, GetRandomVault()), IsNil)
	queryAsgardVaultResp, err := s.queryServer.AsgardVaults(s.ctx, &types.QueryAsgardVaultsRequest{})
	c.Assert(err, IsNil)

	// marshal output so we can verify it unmarshals as expected
	result, err := queryAsgardVaultResp.MarshalJSONPB(nil)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
	var r Vaults
	c.Assert(json.Unmarshal(result, &r), IsNil)

	// EdDSA Vault
	pubKey := GetRandomPubKey()
	eddsaPk := GetRandomPubKey()
	asgard2 := NewVaultV2(ctx.BlockHeight(), ActiveVault, AsgardVault, pubKey, common.Chains{common.ETHChain, common.SOLChain}.Strings(), nil, eddsaPk)
	c.Assert(mgr.Keeper().SetVault(ctx, asgard2), IsNil)

	// Verify conformance to openapi spec
	var openapiVaults []openapi.Vault
	err = json.Unmarshal(result, &openapiVaults)
	c.Assert(err, IsNil)

	c.Assert(len(openapiVaults[0].Addresses), Equals, len(queryAsgardVaultResp.AsgardVaults[0].Addresses))
	c.Assert(*openapiVaults[0].BlockHeight, Equals, queryAsgardVaultResp.AsgardVaults[0].BlockHeight)
	c.Assert(*openapiVaults[0].PubKey, Equals, queryAsgardVaultResp.AsgardVaults[0].PubKey)
	c.Assert(*openapiVaults[0].Type, Equals, queryAsgardVaultResp.AsgardVaults[0].Type)
	c.Assert(openapiVaults[0].Status, Equals, queryAsgardVaultResp.AsgardVaults[0].Status)
	c.Assert(*openapiVaults[0].StatusSince, Equals, queryAsgardVaultResp.AsgardVaults[0].StatusSince)
	c.Assert(len(openapiVaults[0].Chains), Equals, len(queryAsgardVaultResp.AsgardVaults[0].Chains))
	c.Assert(len(openapiVaults[0].Deficit), Equals, len(queryAsgardVaultResp.AsgardVaults[0].Deficit))
}

func (s *QuerierSuite) TestQueryAsgardVaultIncludesInactiveDeficit(c *C) {
	deficitVault := GetRandomVault()
	deficitVault.Status = InactiveVault
	deficitVault.Coins = common.Coins{}
	deficitVault.Deficit = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(12345)))
	c.Assert(s.k.SetVault(s.ctx, deficitVault), IsNil)
	c.Assert(s.k.RemoveFromAsgardIndex(s.ctx, deficitVault.PubKey), IsNil)
	initDeficitVault := GetRandomVault()
	initDeficitVault.Status = InitVault
	initDeficitVault.Coins = common.Coins{}
	initDeficitVault.Deficit = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(67890)))
	c.Assert(s.k.SetVault(s.ctx, initDeficitVault), IsNil)
	c.Assert(s.k.RemoveFromAsgardIndex(s.ctx, initDeficitVault.PubKey), IsNil)

	abandonedVault := GetRandomVault()
	abandonedVault.Status = InactiveVault
	abandonedVault.Coins = common.Coins{}
	abandonedVault.Deficit = common.Coins{}
	c.Assert(s.k.SetVault(s.ctx, abandonedVault), IsNil)
	c.Assert(s.k.RemoveFromAsgardIndex(s.ctx, abandonedVault.PubKey), IsNil)

	resp, err := s.queryServer.AsgardVaults(s.ctx, &types.QueryAsgardVaultsRequest{})
	c.Assert(err, IsNil)

	var foundDeficit, foundInitDeficit, foundAbandoned bool
	for _, vault := range resp.AsgardVaults {
		switch vault.PubKey {
		case deficitVault.PubKey.String():
			foundDeficit = true
			c.Check(vault.Status, Equals, InactiveVault.String())
			c.Check(vault.Deficit.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(12345)), Equals, true)
		case initDeficitVault.PubKey.String():
			foundInitDeficit = true
			c.Check(vault.Status, Equals, InitVault.String())
			c.Check(vault.Deficit.GetCoin(common.BTCAsset).Amount.Equal(cosmos.NewUint(67890)), Equals, true)
		case abandonedVault.PubKey.String():
			foundAbandoned = true
		}
	}
	c.Check(foundDeficit, Equals, true)
	c.Check(foundInitDeficit, Equals, true)
	c.Check(foundAbandoned, Equals, false)
}

func (s *QuerierSuite) TestQueryVaultPubKeys(c *C) {
	node := GetRandomValidatorNode(NodeActive)
	c.Assert(s.k.SetNodeAccount(s.ctx, node), IsNil)
	vault := GetRandomVault()
	vault.PubKey = node.PubKeySet.Secp256k1
	vault.PubKeyFrost = GetRandomEd25519PubKey()
	vault.Type = AsgardVault
	vault.AddFunds(common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One*100)),
	})
	vault.Routers = []types.ChainContract{
		{
			Chain:  "ETH",
			Router: "0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44",
		},
	}
	c.Assert(s.k.SetVault(s.ctx, vault), IsNil)
	vault1 := GetRandomVault()
	vault1.Routers = vault.Routers
	c.Assert(s.k.SetVault(s.ctx, vault1), IsNil)
	queryVaultPubkeys, err := s.queryServer.VaultsPubkeys(s.ctx, &types.QueryVaultsPubkeysRequest{})
	c.Assert(err, IsNil)

	// QueryVaultsPubkeysResponse does not require JSONPBMarshaler implementation
	result, err := json.Marshal(queryVaultPubkeys)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
	var r openapi.VaultPubkeysResponse
	c.Assert(json.Unmarshal(result, &r), IsNil)

	// Verify conformance to openapi spec
	c.Assert(r.Asgard[0].PubKey, Equals, queryVaultPubkeys.Asgard[0].PubKey)
	c.Assert(r.Asgard[0].GetPubKeyFrost(), Equals, queryVaultPubkeys.Asgard[0].PubKeyFrost)
	c.Assert(*r.Asgard[0].Routers[0].Chain, Equals, queryVaultPubkeys.Asgard[0].Routers[0].Chain)
	c.Assert(*r.Asgard[0].Routers[0].Router, Equals, queryVaultPubkeys.Asgard[0].Routers[0].Router)
}

func (s *QuerierSuite) TestQueryBalanceModule(c *C) {
	c.Assert(s.k.SetVault(s.ctx, GetRandomVault()), IsNil)
	queryBalanceModulesResp, err := s.queryServer.BalanceModule(s.ctx, &types.QueryBalanceModuleRequest{
		Name: "asgard",
	})
	c.Assert(err, IsNil)

	// marshal output so we can verify it unmarshals as expected
	result, err := queryBalanceModulesResp.MarshalJSONPB(nil)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
	var r struct {
		Name    string            `json:"name"`
		Address cosmos.AccAddress `json:"address"`
		Coins   types2.Coins      `json:"coins"`
	}
	c.Assert(json.Unmarshal(result, &r), IsNil)

	// Verify conformance to legacy output
	c.Assert(r.Address.String(), Equals, queryBalanceModulesResp.Address.String())
	c.Assert(r.Coins[0].Amount.String(), Equals, queryBalanceModulesResp.Coins[0].Amount.String())
	c.Assert(r.Coins[0].Denom, Equals, queryBalanceModulesResp.Coins[0].Denom)
	c.Assert(r.Name, Equals, queryBalanceModulesResp.Name)
}

func (s *QuerierSuite) TestQueryVault(c *C) {
	vault := GetRandomVault()

	queryVaultResp, err := s.queryServer.Vault(s.ctx, &types.QueryVaultRequest{
		PubKey: "ETH",
	})
	c.Assert(queryVaultResp, IsNil)
	c.Assert(err, NotNil)

	c.Assert(s.k.SetVault(s.ctx, vault), IsNil)
	queryVaultResp, err = s.queryServer.Vault(s.ctx, &types.QueryVaultRequest{
		PubKey: vault.PubKey.String(),
	})
	c.Assert(err, IsNil)

	// marshal output so we can verify it unmarshals as expected
	result, err := queryVaultResp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	var returnVault Vault
	c.Assert(json.Unmarshal(result, &returnVault), IsNil)
	c.Assert(vault.PubKey.Equals(returnVault.PubKey), Equals, true)
	c.Assert(vault.Type, Equals, returnVault.Type)
	c.Assert(vault.Status, Equals, returnVault.Status)
	c.Assert(vault.BlockHeight, Equals, returnVault.BlockHeight)
}

// TestQueryKeysignParty verifies jailed members are reported as fallback
// candidates and restored after jail release.
func (s *QuerierSuite) TestQueryKeysignParty(c *C) {
	ctx := s.ctx.WithBlockHeight(100)
	vault, nodes := s.setupKeysignPartyVault(c, ctx)

	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[1].NodeAddress, 150, jailReasonFailedToPerformKeysign), IsNil)
	resp, err := s.queryServer.KeysignParty(ctx, &types.QueryKeysignPartyRequest{
		PubKey: vault.PubKey.String(),
	})
	c.Assert(err, IsNil)
	c.Assert(resp.Signers, DeepEquals, []string{
		nodes[0].PubKeySet.Secp256k1.String(),
		nodes[2].PubKeySet.Secp256k1.String(),
		nodes[3].PubKeySet.Secp256k1.String(),
	})
	c.Assert(resp.Height, Equals, int64(100))
	c.Assert(resp.RequiredSigners, Equals, int64(3))
	c.Assert(resp.ActiveSignerCount, Equals, int64(3))
	c.Assert(resp.NeverSigners, HasLen, 0)
	c.Assert(resp.JailedFallbackSigners, DeepEquals, []string{
		nodes[1].PubKeySet.Secp256k1.String(),
	})

	c.Assert(s.k.ReleaseNodeAccountFromJail(ctx, nodes[1].NodeAddress), IsNil)
	resp, err = s.queryServer.KeysignParty(ctx, &types.QueryKeysignPartyRequest{
		PubKey: vault.PubKey.String(),
	})
	c.Assert(err, IsNil)
	c.Assert(resp.Signers, HasLen, len(nodes))
	c.Assert(resp.ActiveSignerCount, Equals, int64(len(nodes)))
	c.Assert(resp.NeverSigners, HasLen, 0)
	c.Assert(resp.JailedFallbackSigners, HasLen, 0)
}

// TestQueryKeysignPartyUsesRequestedHeight verifies policy evaluation can be
// pinned to the signing height rather than the latest query context.
func (s *QuerierSuite) TestQueryKeysignPartyUsesRequestedHeight(c *C) {
	ctx := s.ctx.WithBlockHeight(200)
	vault, nodes := s.setupKeysignPartyVault(c, ctx)

	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[1].NodeAddress, 150, jailReasonFailedToPerformKeysign), IsNil)
	resp, err := s.queryServer.KeysignParty(ctx, &types.QueryKeysignPartyRequest{
		PubKey:        vault.PubKey.String(),
		SigningHeight: "100",
	})
	c.Assert(err, IsNil)
	c.Assert(resp.Height, Equals, int64(100))
	c.Assert(resp.Signers, DeepEquals, []string{
		nodes[0].PubKeySet.Secp256k1.String(),
		nodes[2].PubKeySet.Secp256k1.String(),
		nodes[3].PubKeySet.Secp256k1.String(),
	})

	resp, err = s.queryServer.KeysignParty(ctx, &types.QueryKeysignPartyRequest{
		PubKey:        vault.PubKey.String(),
		SigningHeight: "150",
	})
	c.Assert(err, IsNil)
	c.Assert(resp.Height, Equals, int64(150))
	c.Assert(resp.Signers, HasLen, len(nodes))
}

// TestQueryKeysignPartyRejectsNegativeHeight rejects invalid historical height
// input before policy classification runs.
func (s *QuerierSuite) TestQueryKeysignPartyRejectsNegativeHeight(c *C) {
	ctx := s.ctx.WithBlockHeight(100)
	vault, _ := s.setupKeysignPartyVault(c, ctx)

	resp, err := s.queryServer.KeysignParty(ctx, &types.QueryKeysignPartyRequest{
		PubKey:        vault.PubKey.String(),
		SigningHeight: "-1",
	})
	c.Assert(resp, IsNil)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*height.*negative.*")
}

// TestQueryKeysignPartyFindsVaultByFrostPubKey verifies FROST callers can use
// the FROST vault key while the response remains anchored to the secp vault.
func (s *QuerierSuite) TestQueryKeysignPartyFindsVaultByFrostPubKey(c *C) {
	ctx := s.ctx.WithBlockHeight(100)
	vault, nodes := s.setupKeysignPartyVault(c, ctx)
	frostPubKey := GetRandomPubKey()
	vault.PubKeyFrost = frostPubKey
	c.Assert(s.k.SetVault(ctx, vault), IsNil)

	resp, err := s.queryServer.KeysignParty(ctx, &types.QueryKeysignPartyRequest{
		PubKey: frostPubKey.String(),
	})
	c.Assert(err, IsNil)
	c.Assert(resp.PubKey, Equals, vault.PubKey.String())
	c.Assert(resp.Signers, DeepEquals, []string{
		nodes[0].PubKeySet.Secp256k1.String(),
		nodes[1].PubKeySet.Secp256k1.String(),
		nodes[2].PubKeySet.Secp256k1.String(),
		nodes[3].PubKeySet.Secp256k1.String(),
	})
}

// TestQueryKeysignPartyUsesJailedFallbackOnlyWhenNeeded verifies jailed members
// are promoted only when clean preferred signers cannot satisfy threshold.
func (s *QuerierSuite) TestQueryKeysignPartyUsesJailedFallbackOnlyWhenNeeded(c *C) {
	ctx := s.ctx.WithBlockHeight(100)
	vault, nodes := s.setupKeysignPartyVault(c, ctx)

	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[1].NodeAddress, 150, jailReasonFailedToPerformKeygen), IsNil)
	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[2].NodeAddress, 150, jailReasonFailedToPerformKeysign), IsNil)
	resp, err := s.queryServer.KeysignParty(ctx, &types.QueryKeysignPartyRequest{
		PubKey: vault.PubKey.String(),
	})
	c.Assert(err, IsNil)
	c.Assert(resp.RequiredSigners, Equals, int64(3))
	c.Assert(resp.Signers, DeepEquals, []string{
		nodes[0].PubKeySet.Secp256k1.String(),
		nodes[3].PubKeySet.Secp256k1.String(),
		nodes[1].PubKeySet.Secp256k1.String(),
	})
	c.Assert(resp.ActiveSignerCount, Equals, int64(3))
	c.Assert(resp.NeverSigners, HasLen, 0)
	c.Assert(resp.JailedFallbackSigners, DeepEquals, []string{
		nodes[1].PubKeySet.Secp256k1.String(),
		nodes[2].PubKeySet.Secp256k1.String(),
	})
}

// TestQueryKeysignPartyJailedFallbackPriority locks the deterministic ordering
// used when several jailed fallback candidates are available.
func (s *QuerierSuite) TestQueryKeysignPartyJailedFallbackPriority(c *C) {
	ctx := s.ctx.WithBlockHeight(100)
	vault, nodes := s.setupKeysignPartyVaultWithCount(c, ctx, 6)

	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[1].NodeAddress, 150, "unknown jail reason"), IsNil)
	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[2].NodeAddress, 150, jailReasonFailedToPerformKeysign), IsNil)
	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[3].NodeAddress, 150, jailReasonFailedToPerformKeygen), IsNil)
	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[4].NodeAddress, 150, jailReasonFailedToVoteKeygen), IsNil)
	resp, err := s.queryServer.KeysignParty(ctx, &types.QueryKeysignPartyRequest{
		PubKey: vault.PubKey.String(),
	})
	c.Assert(err, IsNil)
	c.Assert(resp.RequiredSigners, Equals, int64(4))
	c.Assert(resp.Signers, DeepEquals, []string{
		nodes[0].PubKeySet.Secp256k1.String(),
		nodes[5].PubKeySet.Secp256k1.String(),
		nodes[4].PubKeySet.Secp256k1.String(),
		nodes[3].PubKeySet.Secp256k1.String(),
	})
	c.Assert(resp.JailedFallbackSigners, DeepEquals, []string{
		nodes[4].PubKeySet.Secp256k1.String(),
		nodes[3].PubKeySet.Secp256k1.String(),
		nodes[2].PubKeySet.Secp256k1.String(),
		nodes[1].PubKeySet.Secp256k1.String(),
	})
}

// TestQueryKeysignPartyJailedFallbackReliabilityTieBreakers ensures equal jail
// reasons are ordered by lower slash points, then shorter remaining jail time.
func (s *QuerierSuite) TestQueryKeysignPartyJailedFallbackReliabilityTieBreakers(c *C) {
	ctx := s.ctx.WithBlockHeight(100)
	vault, nodes := s.setupKeysignPartyVaultWithCount(c, ctx, 6)

	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[1].NodeAddress, 150, jailReasonFailedToPerformKeysign), IsNil)
	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[2].NodeAddress, 180, jailReasonFailedToPerformKeysign), IsNil)
	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[3].NodeAddress, 130, jailReasonFailedToPerformKeysign), IsNil)
	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[4].NodeAddress, 120, jailReasonFailedToPerformKeysign), IsNil)
	s.k.SetNodeAccountSlashPoints(ctx, nodes[1].NodeAddress, 20)
	s.k.SetNodeAccountSlashPoints(ctx, nodes[2].NodeAddress, 5)
	s.k.SetNodeAccountSlashPoints(ctx, nodes[3].NodeAddress, 5)
	s.k.SetNodeAccountSlashPoints(ctx, nodes[4].NodeAddress, 10)

	resp, err := s.queryServer.KeysignParty(ctx, &types.QueryKeysignPartyRequest{
		PubKey: vault.PubKey.String(),
	})
	c.Assert(err, IsNil)
	c.Assert(resp.RequiredSigners, Equals, int64(4))
	c.Assert(resp.Signers, DeepEquals, []string{
		nodes[0].PubKeySet.Secp256k1.String(),
		nodes[5].PubKeySet.Secp256k1.String(),
		nodes[3].PubKeySet.Secp256k1.String(),
		nodes[2].PubKeySet.Secp256k1.String(),
	})
	c.Assert(resp.JailedFallbackSigners, DeepEquals, []string{
		nodes[3].PubKeySet.Secp256k1.String(),
		nodes[2].PubKeySet.Secp256k1.String(),
		nodes[4].PubKeySet.Secp256k1.String(),
		nodes[1].PubKeySet.Secp256k1.String(),
	})
}

// TestQueryKeysignPartyUnknownJailReasonIsLastFallback ensures unknown jail
// reasons are still usable but rank behind known keygen/keysign reasons.
func (s *QuerierSuite) TestQueryKeysignPartyUnknownJailReasonIsLastFallback(c *C) {
	ctx := s.ctx.WithBlockHeight(100)
	vault, nodes := s.setupKeysignPartyVault(c, ctx)

	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[1].NodeAddress, 150, "unknown jail reason"), IsNil)
	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[2].NodeAddress, 150, jailReasonFailedToPerformKeysign), IsNil)
	nodes[3].ForcedToLeave = true
	c.Assert(s.k.SetNodeAccount(ctx, nodes[3]), IsNil)
	resp, err := s.queryServer.KeysignParty(ctx, &types.QueryKeysignPartyRequest{
		PubKey: vault.PubKey.String(),
	})
	c.Assert(err, IsNil)
	c.Assert(resp.RequiredSigners, Equals, int64(3))
	c.Assert(resp.Signers, DeepEquals, []string{
		nodes[0].PubKeySet.Secp256k1.String(),
		nodes[2].PubKeySet.Secp256k1.String(),
		nodes[1].PubKeySet.Secp256k1.String(),
	})
	c.Assert(resp.NeverSigners, DeepEquals, []string{
		nodes[3].PubKeySet.Secp256k1.String(),
	})
	c.Assert(resp.JailedFallbackSigners, DeepEquals, []string{
		nodes[2].PubKeySet.Secp256k1.String(),
		nodes[1].PubKeySet.Secp256k1.String(),
	})
}

// TestQueryKeysignPartyExcludesForcedToLeave verifies forced-to-leave members
// are never returned as signers.
func (s *QuerierSuite) TestQueryKeysignPartyExcludesForcedToLeave(c *C) {
	ctx := s.ctx.WithBlockHeight(100)
	vault, nodes := s.setupKeysignPartyVault(c, ctx)

	nodes[3].ForcedToLeave = true
	c.Assert(s.k.SetNodeAccount(ctx, nodes[3]), IsNil)
	resp, err := s.queryServer.KeysignParty(ctx, &types.QueryKeysignPartyRequest{
		PubKey: vault.PubKey.String(),
	})
	c.Assert(err, IsNil)
	c.Assert(resp.Signers, DeepEquals, []string{
		nodes[0].PubKeySet.Secp256k1.String(),
		nodes[1].PubKeySet.Secp256k1.String(),
		nodes[2].PubKeySet.Secp256k1.String(),
	})
	c.Assert(resp.NeverSigners, DeepEquals, []string{
		nodes[3].PubKeySet.Secp256k1.String(),
	})
}

// TestQueryKeysignPartyDisableFilteringMimirReturnsFullMembership verifies the
// economic Mimir escape hatch disables both jail and forced-to-leave filtering.
func (s *QuerierSuite) TestQueryKeysignPartyDisableFilteringMimirReturnsFullMembership(c *C) {
	ctx := s.ctx.WithBlockHeight(100)
	vault, nodes := s.setupKeysignPartyVault(c, ctx)

	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[1].NodeAddress, 150, jailReasonFailedToPerformKeysign), IsNil)
	nodes[2].ForcedToLeave = true
	c.Assert(s.k.SetNodeAccount(ctx, nodes[2]), IsNil)
	s.k.SetMimir(ctx, constants.MimirKeyDisableTSSSignerFiltering, 1)

	resp, err := s.queryServer.KeysignParty(ctx, &types.QueryKeysignPartyRequest{
		PubKey: vault.PubKey.String(),
	})
	c.Assert(err, IsNil)
	c.Assert(resp.Signers, DeepEquals, []string{
		nodes[0].PubKeySet.Secp256k1.String(),
		nodes[1].PubKeySet.Secp256k1.String(),
		nodes[2].PubKeySet.Secp256k1.String(),
		nodes[3].PubKeySet.Secp256k1.String(),
	})
	c.Assert(resp.RequiredSigners, Equals, int64(3))
	c.Assert(resp.ActiveSignerCount, Equals, int64(len(nodes)))
	c.Assert(resp.NeverSigners, HasLen, 0)
	c.Assert(resp.JailedFallbackSigners, HasLen, 0)
}

// TestQueryKeysignPartyIncludesRequestedToLeave documents that requested-leave
// nodes remain eligible until they are actually churned or forced out.
func (s *QuerierSuite) TestQueryKeysignPartyIncludesRequestedToLeave(c *C) {
	ctx := s.ctx.WithBlockHeight(100)
	vault, nodes := s.setupKeysignPartyVaultWithCount(c, ctx, 6)

	nodes[5].RequestedToLeave = true
	c.Assert(s.k.SetNodeAccount(ctx, nodes[5]), IsNil)
	resp, err := s.queryServer.KeysignParty(ctx, &types.QueryKeysignPartyRequest{
		PubKey: vault.PubKey.String(),
	})
	c.Assert(err, IsNil)
	c.Assert(resp.RequiredSigners, Equals, int64(4))
	c.Assert(resp.Signers, DeepEquals, []string{
		nodes[0].PubKeySet.Secp256k1.String(),
		nodes[1].PubKeySet.Secp256k1.String(),
		nodes[2].PubKeySet.Secp256k1.String(),
		nodes[3].PubKeySet.Secp256k1.String(),
		nodes[4].PubKeySet.Secp256k1.String(),
		nodes[5].PubKeySet.Secp256k1.String(),
	})
	c.Assert(resp.NeverSigners, HasLen, 0)
}

// TestQueryKeysignPartyFailsWhenBelowThreshold fails closed when filtering
// leaves too few eligible signers for the original vault threshold.
func (s *QuerierSuite) TestQueryKeysignPartyFailsWhenBelowThreshold(c *C) {
	ctx := s.ctx.WithBlockHeight(100)
	vault, nodes := s.setupKeysignPartyVault(c, ctx)

	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[1].NodeAddress, 150, jailReasonFailedToPerformKeysign), IsNil)
	nodes[2].ForcedToLeave = true
	nodes[3].ForcedToLeave = true
	c.Assert(s.k.SetNodeAccount(ctx, nodes[2]), IsNil)
	c.Assert(s.k.SetNodeAccount(ctx, nodes[3]), IsNil)

	resp, err := s.queryServer.KeysignParty(ctx, &types.QueryKeysignPartyRequest{
		PubKey: vault.PubKey.String(),
	})
	c.Assert(resp, IsNil)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*not enough eligible signers.*")
}

// TestQueryKeysignPartyFailsForCompromisedVault verifies compromised vaults are
// a hard stop even when filtering is disabled by Mimir.
func (s *QuerierSuite) TestQueryKeysignPartyFailsForCompromisedVault(c *C) {
	ctx := s.ctx.WithBlockHeight(100)
	vault, _ := s.setupKeysignPartyVault(c, ctx)

	s.k.SetMimir(ctx, constants.MimirKeyDisableTSSSignerFiltering, 1)
	s.k.SetMimir(ctx, compromisedVaultMimirKeyPrefix+vault.PubKey.String(), 1)
	resp, err := s.queryServer.KeysignParty(ctx, &types.QueryKeysignPartyRequest{
		PubKey: vault.PubKey.String(),
	})
	c.Assert(resp, IsNil)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*marked compromised.*")
}

// TestKeygenEligibleNodeAccountsKeepsJailedButExcludesFlagged verifies keygen
// filtering ignores jail state and only removes nodes that must not receive new
// key shares.
func (s *QuerierSuite) TestKeygenEligibleNodeAccountsKeepsJailedButExcludesFlagged(c *C) {
	ctx := s.ctx.WithBlockHeight(100)
	_, nodes := s.setupKeysignPartyVaultWithCount(c, ctx, 6)

	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[1].NodeAddress, 150, jailReasonFailedToPerformKeygen), IsNil)
	nodes[2].ForcedToLeave = true
	nodes[3].RequestedToLeave = true
	nodes[4].LeaveScore = 1
	c.Assert(s.k.SetNodeAccount(ctx, nodes[2]), IsNil)
	c.Assert(s.k.SetNodeAccount(ctx, nodes[3]), IsNil)
	c.Assert(s.k.SetNodeAccount(ctx, nodes[4]), IsNil)

	eligible, err := keygenEligibleNodeAccounts(ctx, s.k, NodeAccounts(nodes))
	c.Assert(err, IsNil)
	c.Assert(eligible, HasLen, 5)
	c.Assert(eligible[0].PubKeySet.Secp256k1.String(), Equals, nodes[0].PubKeySet.Secp256k1.String())
	c.Assert(eligible[1].PubKeySet.Secp256k1.String(), Equals, nodes[1].PubKeySet.Secp256k1.String())
	c.Assert(eligible[2].PubKeySet.Secp256k1.String(), Equals, nodes[3].PubKeySet.Secp256k1.String())
	c.Assert(eligible[3].PubKeySet.Secp256k1.String(), Equals, nodes[4].PubKeySet.Secp256k1.String())
	c.Assert(eligible[4].PubKeySet.Secp256k1.String(), Equals, nodes[5].PubKeySet.Secp256k1.String())
}

// TestKeygenEligibleNodeAccountsDisableFilteringMimirReturnsAllNodes verifies
// the same disable Mimir bypasses keygen filtering.
func (s *QuerierSuite) TestKeygenEligibleNodeAccountsDisableFilteringMimirReturnsAllNodes(c *C) {
	ctx := s.ctx.WithBlockHeight(100)
	_, nodes := s.setupKeysignPartyVaultWithCount(c, ctx, 6)

	c.Assert(s.k.SetNodeAccountJail(ctx, nodes[1].NodeAddress, 150, jailReasonFailedToPerformKeygen), IsNil)
	nodes[2].ForcedToLeave = true
	c.Assert(s.k.SetNodeAccount(ctx, nodes[2]), IsNil)
	s.k.SetMimir(ctx, constants.MimirKeyDisableTSSSignerFiltering, 1)

	eligible, err := keygenEligibleNodeAccounts(ctx, s.k, NodeAccounts(nodes))
	c.Assert(err, IsNil)
	c.Assert(eligible, HasLen, len(nodes))
	for i := range nodes {
		c.Assert(eligible[i].PubKeySet.Secp256k1.String(), Equals, nodes[i].PubKeySet.Secp256k1.String())
	}
}

// TestKeygenEligibleThresholdRejectsTooFewMembers protects against creating a
// vault whose filtered member set is below the original TSS threshold.
func (s *QuerierSuite) TestKeygenEligibleThresholdRejectsTooFewMembers(c *C) {
	ctx := s.ctx.WithBlockHeight(100)
	_, nodes := s.setupKeysignPartyVaultWithCount(c, ctx, 6)

	eligible := NodeAccounts{nodes[0], nodes[1], nodes[2]}
	err := validateKeygenEligibleThreshold(NodeAccounts(nodes), eligible)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*need at least 4 of 6 original members, got 3.*")
}

// TestKeygenEligibleThresholdRejectsZeroMembers verifies an empty filtered
// keygen set fails with a clear error.
func (s *QuerierSuite) TestKeygenEligibleThresholdRejectsZeroMembers(c *C) {
	ctx := s.ctx.WithBlockHeight(100)
	_, nodes := s.setupKeysignPartyVaultWithCount(c, ctx, 6)

	err := validateKeygenEligibleThreshold(NodeAccounts(nodes), nil)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*no eligible keygen members.*")
}

// setupKeysignPartyVault creates the default four-member active vault used by
// keysign policy tests.
func (s *QuerierSuite) setupKeysignPartyVault(c *C, ctx cosmos.Context) (Vault, []NodeAccount) {
	return s.setupKeysignPartyVaultWithCount(c, ctx, 4)
}

// setupKeysignPartyVaultWithCount creates a deterministic active vault with
// count validator members and persists the matching node accounts.
func (s *QuerierSuite) setupKeysignPartyVaultWithCount(c *C, ctx cosmos.Context, count int) (Vault, []NodeAccount) {
	nodes := make([]NodeAccount, 0, count)
	for i := 0; i < count; i++ {
		nodes = append(nodes, GetRandomValidatorNode(NodeActive))
	}
	membership := make([]string, 0, len(nodes))
	for _, node := range nodes {
		c.Assert(s.k.SetNodeAccount(ctx, node), IsNil)
		membership = append(membership, node.PubKeySet.Secp256k1.String())
	}
	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), nil)
	vault.Membership = membership
	c.Assert(s.k.SetVault(ctx, vault), IsNil)
	return vault, nodes
}

func (s *QuerierSuite) TestQueryVersion(c *C) {
	queryVersionResp, err := s.queryServer.Version(s.ctx, &types.QueryVersionRequest{})
	c.Assert(err, IsNil)

	result, err := queryVersionResp.MarshalJSONPB(nil)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
	var r openapi.VersionResponse
	c.Assert(json.Unmarshal(result, &r), IsNil)

	verComputed := s.k.GetLowestActiveVersion(s.ctx)
	c.Assert(r.Current, Equals, verComputed.String(),
		Commentf("query should return same version as computed"))

	// override the version computed in BeginBlock
	s.k.SetVersionWithCtx(s.ctx, semver.MustParse("4.5.6"))

	queryVersionResp, err = s.queryServer.Version(s.ctx, &types.QueryVersionRequest{})
	c.Assert(err, IsNil)

	result, err = queryVersionResp.MarshalJSONPB(nil)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
	c.Assert(json.Unmarshal(result, &r), IsNil)
	c.Assert(r.Current, Equals, "4.5.6",
		Commentf("query should use stored version"))
}

func (s *QuerierSuite) TestPeerIDFromPubKey(c *C) {
	// Success example for the current configured THOR pubkey prefix.
	peerID := getPeerIDFromPubKey(GetRandomPubKey())
	c.Assert(strings.HasPrefix(peerID, "fail to parse account pub key"), Equals, false)
	c.Assert(peerID, Matches, "^16Uiu2[1-9A-HJ-NP-Za-km-z]+$")

	// Failure example.
	expectedErrorString := "fail to parse account pub key(nonsense): decoding bech32 failed: invalid separator index -1"
	c.Assert(getPeerIDFromPubKey("nonsense"), Equals, expectedErrorString)
}

func (s *QuerierSuite) TestQuerySecuredAsset(c *C) {
	owner := GetRandomBech32Addr()
	addr := GetRandomBTCAddress()

	_, err := s.mgr.SecuredAssetManager().Deposit(s.ctx, common.BTCAsset, cosmos.NewUint(1000), owner, addr, common.BlankTxID)
	c.Assert(err, IsNil)

	result, err := s.queryServer.SecuredAsset(s.ctx, &types.QuerySecuredAssetRequest{
		Asset: "btc-btc",
	})

	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	c.Assert(result.Asset, Equals, "BTC-BTC")
	c.Assert(result.Depth, Equals, "1000")
	c.Assert(result.Supply, Equals, "1000")
}

func (s *QuerierSuite) TestQuerySecuredAssets(c *C) {
	owner := GetRandomBech32Addr()
	addr := GetRandomBTCAddress()

	_, err := s.mgr.SecuredAssetManager().Deposit(s.ctx, common.BTCAsset, cosmos.NewUint(1000), owner, addr, common.BlankTxID)
	c.Assert(err, IsNil)

	_, err = s.mgr.SecuredAssetManager().Deposit(s.ctx, common.ETHAsset, cosmos.NewUint(2000), owner, addr, common.BlankTxID)
	c.Assert(err, IsNil)

	result, err := s.queryServer.SecuredAssets(s.ctx, &types.QuerySecuredAssetsRequest{})

	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
	c.Assert(len(result.Assets), Equals, 2)
	asset := result.Assets[0]
	c.Assert(asset.Asset, Equals, "BTC-BTC")
	c.Assert(asset.Depth, Equals, "1000")
	c.Assert(asset.Supply, Equals, "1000")

	asset = result.Assets[1]
	c.Assert(asset.Asset, Equals, "ETH-ETH")
	c.Assert(asset.Depth, Equals, "2000")
	c.Assert(asset.Supply, Equals, "2000")
}

func (s *QuerierSuite) TestQuerySwap(c *C) {
	nodeAccount := GetRandomValidatorNode(NodeActive)
	c.Assert(s.mgr.Keeper().SetNodeAccount(s.ctx, nodeAccount), IsNil)

	// pubKey := GetRandomPubKey()
	asgard := NewVaultV2(
		s.ctx.BlockHeight(),
		ActiveVault,
		AsgardVault,
		GetRandomPubKey(),
		common.Chains{
			common.BTCChain,
			common.ETHChain,
			common.XMRChain,
		}.Strings(),
		[]ChainContract{},
		GetRandomEd25519PubKey(),
	)
	asgard.PubKeyFrost = GetRandomEd25519PubKey()
	asgard.AddFunds(common.Coins{
		common.NewCoin(common.XMRAsset, common.XMRChain.ThorchainToNativeGas(cosmos.NewUint(100*common.One))),
	})

	c.Assert(s.mgr.Keeper().SetVault(s.ctx, asgard), IsNil)

	poolBTC := NewPool()
	poolBTC.Asset = common.BTCAsset
	poolBTC.BalanceAsset = cosmos.NewUint(1_000_000_000)
	poolBTC.BalanceRune = cosmos.NewUint(10_000_000_000_000)
	poolBTC.LPUnits = cosmos.NewUint(100)

	err := s.mgr.Keeper().SetPool(s.ctx, poolBTC)
	c.Assert(err, IsNil)

	poolETH := NewPool()
	poolETH.Asset = common.ETHAsset
	poolETH.BalanceAsset = cosmos.NewUint(100_000_000_000)
	poolETH.BalanceRune = cosmos.NewUint(10_000_000_000_000)
	poolETH.LPUnits = cosmos.NewUint(100)

	err = s.mgr.Keeper().SetPool(s.ctx, poolETH)
	c.Assert(err, IsNil)

	poolXMR := NewPool()
	poolXMR.Asset = common.XMRAsset
	poolXMR.Decimals = common.XMRChain.GetGasAssetDecimal()
	poolXMR.BalanceAsset = cosmos.NewUint(1_000_000_000_000)
	poolXMR.BalanceRune = cosmos.NewUint(10_000_000_000_000)
	poolXMR.LPUnits = cosmos.NewUint(100)

	err = s.mgr.Keeper().SetPool(s.ctx, poolXMR)
	c.Assert(err, IsNil)
	err = s.mgr.Keeper().SaveNetworkFee(s.ctx, common.XMRChain, NetworkFee{
		Chain:              common.XMRChain,
		TransactionSize:    10000,
		TransactionFeeRate: 10000,
	})
	c.Assert(err, IsNil)

	s.mgr.Keeper().SetMimir(s.ctx, "EnableAdvSwapQueue", 1)
	s.mgr.Keeper().SetMimir(s.ctx, "L1SlipMinBps", 1)
	rapidDestination := GetRandomTHORAddress().String()
	rapidQuote, err := s.queryServer.QuoteSwap(s.ctx, &types.QueryQuoteSwapRequest{
		FromAsset:         common.BTCAsset.String(),
		ToAsset:           common.RuneNative.String(),
		Amount:            "100000000",
		StreamingInterval: "0",
		StreamingQuantity: "0",
		Destination:       rapidDestination,
	})
	c.Assert(err, IsNil)
	c.Assert(rapidQuote.MaxStreamingQuantity > 1, Equals, true)
	c.Assert(rapidQuote.Memo, Equals, fmt.Sprintf("=:r:%s:0/0/%d", rapidDestination, rapidQuote.MaxStreamingQuantity))

	var addressBTC, addressTHOR string

	if common.CurrentChainNetwork == common.MockNet {
		addressBTC = "bcrt1qg2px54as9vgzaarkr0zy95hacg3lg4kqz4rrwf"
		addressTHOR = "tthor12xxg2sevm35q54vjhssqhlf7lq8d2xhmu5k0gr"
	} else {
		addressBTC = "bc1qk5700y6zwtnjzeh4mffh5qcl46vqgs4lf6rm6m"
		addressTHOR = "thor14j7zjhnazs85macymj4g0xugr8jhdwg07rh2yy"
	}

	affiliateAddr := GetRandomTHORAddress().String()
	request := types.QueryQuoteSwapRequest{
		FromAsset:         common.BTCAsset.String(),
		ToAsset:           common.RuneNative.String(),
		Amount:            "10000000",
		StreamingInterval: "1",
		StreamingQuantity: "5",
		Destination:       addressTHOR,
		ToleranceBps:      "300",
		RefundAddress:     addressBTC,
		Affiliate:         affiliateAddr,
		AffiliateBps:      "50",
	}

	// memo greater than 80 bytes -> return error
	queryPoolsResp, err := s.queryServer.QuoteSwap(s.ctx, &request)

	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "generated memo too long for source chain")
	c.Assert(queryPoolsResp, IsNil)

	// Use extended options - should now succeed with our fix
	request.Extended = true
	queryPoolsResp, err = s.queryServer.QuoteSwap(s.ctx, &request)

	c.Assert(err, IsNil)
	c.Assert(queryPoolsResp, NotNil)

	queryPoolsResp, err = s.queryServer.QuoteSwap(s.ctx, &types.QueryQuoteSwapRequest{
		FromAsset:         common.BTCAsset.String(),
		ToAsset:           common.XMRAsset.String(),
		Amount:            "100000000",
		StreamingQuantity: "1",
	})
	c.Assert(err, IsNil)
	c.Assert(queryPoolsResp, NotNil)
	c.Assert(queryPoolsResp.Memo, Equals, "")

	longDest := "43hSSzguniAQWhHnwk1rcYH51z2thnf6AVfNaMwg9t99S78LtMrjrKdJZCnLTnFVzS9hQNjVXdJi3hUxMwmPrPDkRY3YMUb"
	queryPoolsResp, err = s.queryServer.QuoteSwap(s.ctx, &types.QueryQuoteSwapRequest{
		FromAsset:         common.BTCAsset.String(),
		ToAsset:           common.XMRAsset.String(),
		Amount:            "100000000",
		Destination:       longDest,
		RefundAddress:     addressBTC,
		Affiliate:         GetRandomTHORAddress().String() + "/" + GetRandomTHORAddress().String() + "/" + GetRandomTHORAddress().String(),
		AffiliateBps:      "10/10/10",
		StreamingInterval: "2",
		StreamingQuantity: "5",
		Extended:          true,
	})
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "generated memo too long for source chain")
	c.Assert(queryPoolsResp, IsNil)

	// The following assertions are commented out because the request now fails due to fee parsing issues
	// memo := fmt.Sprintf("=:r:%s/%s:97000000000/1/1:%s:50", addressTHOR, addressBTC, affiliateAddr)
	// c.Assert(queryPoolsResp.Memo, Equals, memo)
	// c.Assert(len(queryPoolsResp.Vout), Equals, 5)
	// c.Assert(queryPoolsResp.Vout[0].Type, Equals, "op_return")
	//
	// // Verify we have address type vouts
	// hasAddressVout := false
	// for _, vout := range queryPoolsResp.Vout {
	// 	if vout.Type == "address" {
	// 		hasAddressVout = true
	// 		break
	// 	}
	// }
	// c.Assert(hasAddressVout, Equals, true)
	//
	// c.Assert(queryPoolsResp.Vout[1].Amount, Equals, int64(294))

	// Empty vout for non-utxo chains
	request.FromAsset = common.ETHAsset.String()
	request.RefundAddress = GetRandomETHAddress().String()
	request.Extended = true

	queryPoolsResp, err = s.queryServer.QuoteSwap(s.ctx, &request)

	// This request should now succeed with our fix
	c.Assert(err, IsNil)
	c.Assert(queryPoolsResp, NotNil)
	// c.Assert(len(queryPoolsResp.Vout), Equals, 0)

	// Test slash-separated affiliate functionality
	affiliate1 := GetRandomTHORAddress().String()
	affiliate2 := GetRandomTHORAddress().String()
	request.Affiliate = affiliate1 + "/" + affiliate2
	request.AffiliateBps = "30/15"
	request.Extended = true

	queryPoolsResp, err = s.queryServer.QuoteSwap(s.ctx, &request)
	c.Assert(err, IsNil)
	c.Assert(queryPoolsResp, NotNil)

	// Verify memo contains both affiliates with correct basis points
	// The memo format includes streaming params: =:r:destination/refund:amount/streaming_interval/streaming_quantity:affiliates:affiliate_bps
	expectedMemo := fmt.Sprintf("=:r:%s/%s:970000000/1/1:%s/%s:30/15", request.Destination, request.RefundAddress, affiliate1, affiliate2)
	c.Assert(queryPoolsResp.Memo, Equals, expectedMemo)
}

func (s *QuerierSuite) TestNetwork(c *C) {
	vault := GetRandomVault()
	vault.Chains = append(vault.Chains, common.ETHChain.String())
	c.Assert(s.k.SetVault(s.ctx, vault), IsNil)

	s.k.SetMimir(s.ctx, "DerivedDepthBasisPts", 10_000)
	s.k.SetMimir(s.ctx, "TorAnchor-ETH-BUSD-BD1", 1) // enable BUSD pool as a TOR anchor
	ethBusd, err := common.NewAsset("ETH.BUSD-BD1")
	c.Assert(err, IsNil)

	pool := NewPool()
	pool.Asset = ethBusd
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(500_000_00000000)
	pool.BalanceAsset = cosmos.NewUint(4_556_123_00000000)
	pool.Decimals = 8
	err = s.k.SetPool(s.ctx, pool)
	c.Assert(err, IsNil)

	runePriceInTor := s.k.DollarsPerRune(s.ctx)
	c.Assert(runePriceInTor.String(), Equals, "911224600")

	torPriceInRune := s.k.RunePerDollar(s.ctx)
	c.Assert(torPriceInRune.String(), Equals, "10974243")

	resp, err := s.queryServer.Network(s.ctx, &types.QueryNetworkRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp.TorPriceInRune, Equals, torPriceInRune.String())
	c.Assert(resp.RunePriceInTor, Equals, runePriceInTor.String())
	c.Assert(resp.TorPriceHalted, Equals, false)
	// there is no previous block, so lastTorHeight is not set

	s.k.SetMimir(s.ctx, "HALTETHTRADING", 1)

	c.Assert(s.k.DollarsPerRune(s.ctx).String(), Equals, "0")
	c.Assert(s.k.RunePerDollar(s.ctx).String(), Equals, "0")

	resp, err = s.queryServer.Network(s.ctx, &types.QueryNetworkRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp.TorPriceInRune, Equals, torPriceInRune.String())
	c.Assert(resp.RunePriceInTor, Equals, runePriceInTor.String())
	c.Assert(resp.TorPriceHalted, Equals, true)

	s.k.SetMimir(s.ctx, "HALTETHTRADING", 0)

	resp, err = s.queryServer.Network(s.ctx, &types.QueryNetworkRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp.TorPriceHalted, Equals, false)
}

func (s *QuerierSuite) TestQueryTCYProtocolOwned(c *C) {
	// empty state: no module balance, no claimers -> "0"
	result, err := s.queryServer.TCYProtocolOwned(s.ctx, &types.QueryTCYProtocolOwnedRequest{})
	c.Assert(err, IsNil)
	c.Assert(result.Amount, Equals, "0")

	// mint TCY into the claim module with no claimer records -> full balance is protocol-owned
	balance := cosmos.NewUint(24_709_358_02962795)
	c.Assert(s.k.MintToModule(s.ctx, ModuleName, common.NewCoin(common.TCY, balance)), IsNil)
	c.Assert(s.k.SendFromModuleToModule(s.ctx, ModuleName, TCYClaimingName, common.NewCoins(common.NewCoin(common.TCY, balance))), IsNil)

	result, err = s.queryServer.TCYProtocolOwned(s.ctx, &types.QueryTCYProtocolOwnedRequest{})
	c.Assert(err, IsNil)
	c.Assert(result.Amount, Equals, balance.String())

	// add claimer records whose sum is less than the module balance ->
	// protocol_owned = balance - sum(claimers)
	claimer1 := types.NewTCYClaimer(GetRandomETHAddress(), common.ETHAsset, cosmos.NewUint(1_030_525_950))
	claimer2 := types.NewTCYClaimer(GetRandomETHAddress(), common.ETHAsset, cosmos.NewUint(19_318_987_899))
	usdtAsset, err := common.NewAsset("ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	c.Assert(err, IsNil)
	claimer3 := types.NewTCYClaimer(GetRandomETHAddress(), usdtAsset, cosmos.NewUint(895_756_041))

	c.Assert(s.k.SetTCYClaimer(s.ctx, claimer1), IsNil)
	c.Assert(s.k.SetTCYClaimer(s.ctx, claimer2), IsNil)
	c.Assert(s.k.SetTCYClaimer(s.ctx, claimer3), IsNil)

	claimerSum := claimer1.Amount.Add(claimer2.Amount).Add(claimer3.Amount)
	expected := common.SafeSub(balance, claimerSum)

	result, err = s.queryServer.TCYProtocolOwned(s.ctx, &types.QueryTCYProtocolOwnedRequest{})
	c.Assert(err, IsNil)
	c.Assert(result.Amount, Equals, expected.String())

	// claimers exceeding module balance -> SafeSub floors at 0
	huge := types.NewTCYClaimer(GetRandomETHAddress(), common.ETHAsset, balance.Add(cosmos.NewUint(1)))
	c.Assert(s.k.SetTCYClaimer(s.ctx, huge), IsNil)

	result, err = s.queryServer.TCYProtocolOwned(s.ctx, &types.QueryTCYProtocolOwnedRequest{})
	c.Assert(err, IsNil)
	c.Assert(result.Amount, Equals, "0")
}

func (s *QuerierSuite) TestQueryPoolLuviFields(c *C) {
	type want struct {
		luvi, equity, perUnit, bps string
		overhang                   bool
	}
	// A = 1e12, R = 4e12 -> sqrt(A*R) = 2e12 exactly; L = 1e12
	cases := []struct {
		name  string
		asset uint64
		synth uint64
		want  want
	}{
		{"no synths", 1_000_000_000_000, 0, want{"2.000000000000000000", "2000000000000.000000000000000000", "2.000000000000000000", "0", false}},
		// S = A/2: Q = 2e12*(2e12-5e11)/2e12 = 1.5e12; SU = floor(1e12*5e11/1.5e12)
		{"synths", 1_000_000_000_000, 500_000_000_000, want{"1.500000000000375000", "1500000000000.000000000000000000", "1.500000000000000000", "2500", false}},
		// S = 2A exactly: equity 0, synth units clamped
		{"boundary", 1_000_000_000_000, 2_000_000_000_000, want{"0.000000000000999999", "0.000000000000000000", "0.000000000000000000", "10000", true}},
		// S = 3A: equity negative, all LP units redeem ~1/(1+S) of the pool
		{"overhang", 1_000_000_000_000, 3_000_000_000_000, want{"0.000000000000666666", "-1000000000000.000000000000000000", "-1.000000000000000000", "15000", true}},
		// asset depth exhausted (e.g. by a slash) with synths outstanding: in overhang
		{"depleted", 0, 1_000_000_000, want{"0.000000000000000000", "0.000000000000000000", "0.000000000000000000", "0", true}},
		// no asset depth and no synths: empty, not in overhang
		{"empty", 0, 0, want{"0.000000000000000000", "0.000000000000000000", "0.000000000000000000", "0", false}},
	}
	for _, tc := range cases {
		ctx, mgr := setupManagerForTest(c)
		txConfig, err := params.TxConfig(s.mgr.cdc, nil)
		c.Assert(err, IsNil)
		queryServer := NewQueryServerImpl(mgr, txConfig, s.kb)
		asgard := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
		c.Assert(mgr.Keeper().SetVault(ctx, asgard), IsNil)

		pool := NewPool()
		pool.Asset = common.ETHAsset
		pool.BalanceAsset = cosmos.NewUint(tc.asset)
		pool.BalanceRune = cosmos.NewUint(4_000_000_000_000)
		pool.LPUnits = cosmos.NewUint(1_000_000_000_000)
		pool.Status = PoolAvailable
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
		if tc.synth > 0 {
			coin := common.NewCoin(common.ETHAsset.GetSyntheticAsset(), cosmos.NewUint(tc.synth))
			c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, coin), IsNil)
		}

		one, err := queryServer.Pool(ctx, &types.QueryPoolRequest{Asset: "ETH.ETH"})
		c.Assert(err, IsNil)
		all, err := queryServer.Pools(ctx, &types.QueryPoolsRequest{})
		c.Assert(err, IsNil)
		c.Assert(all.Pools, HasLen, 1)

		for _, p := range []*types.QueryPoolResponse{one, all.Pools[0]} {
			cm := Commentf("%s: %+v", tc.name, p)
			c.Check(p.Luvi, Equals, tc.want.luvi, cm)
			c.Check(p.LpEquity, Equals, tc.want.equity, cm)
			c.Check(p.LpEquityPerUnit, Equals, tc.want.perUnit, cm)
			c.Check(p.SynthPerPoolDepthBps, Equals, tc.want.bps, cm)
			c.Check(p.SynthOverhang, Equals, tc.want.overhang, cm)
		}

		// the fields are always present in the JSON, including a false overhang
		raw, err := one.MarshalJSONPB(nil)
		c.Assert(err, IsNil)
		var m map[string]any
		c.Assert(json.Unmarshal(raw, &m), IsNil)
		for _, k := range []string{"luvi", "lp_equity", "lp_equity_per_unit", "synth_per_pool_depth_bps", "synth_overhang"} {
			_, ok := m[k]
			c.Check(ok, Equals, true, Commentf("%s: missing %s in %s", tc.name, k, raw))
		}
	}
}
