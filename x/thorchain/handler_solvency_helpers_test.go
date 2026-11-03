package thorchain

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	. "gopkg.in/check.v1"
)

func TestSolvencyHeightTelemetryNormalizesChainLabels(t *testing.T) {
	ctx, mgr := setupBenchmarkManager(t)
	ctx = ctx.WithBlockHeight(1024)
	vault := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), []string{common.TRONChain.String()}, nil)
	if err := mgr.Keeper().SetVault(ctx, vault); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Keeper().SetLastChainHeight(ctx, common.TRONChain, 2000); err != nil {
		t.Fatal(err)
	}
	active := NodeAccounts{
		GetRandomValidatorNode(NodeActive), GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive), GetRandomValidatorNode(NodeActive),
	}
	sink := installRecordingSink(t)
	for _, chain := range []common.Chain{common.TRONChain, "tron", "TrOn"} {
		voter := NewSolvencyVoter(GetRandomTxHash(), chain, vault.PubKey, nil, 1900)
		solvency := &common.Solvency{
			Id: voter.Id, Chain: chain, PubKey: vault.PubKey, Height: voter.Height,
		}
		for _, node := range active[:2] {
			if err := processSolvencyAttestation(ctx, mgr, &voter, node.NodeAddress, active, solvency, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := sink.gauges["thornode.solvency.last_chain_height_delta{chain=TRON}"]; got != 100 {
		t.Fatalf("expected normalized height delta of 100, got %v", got)
	}
	if got := sink.gaugesWithPrefix("thornode.solvency.last_chain_height_delta"); got != 1 {
		t.Fatalf("chain casing must share one height-delta series, got %d", got)
	}
	if got := sink.counters["thornode.solvency.height_check_total{chain=TRON,result=stale}"]; got != 3 {
		t.Fatalf("expected three stale checks in one normalized series, got %v", got)
	}
	if got := countersWithPrefix(sink, "thornode.solvency.height_check_total"); got != 1 {
		t.Fatalf("chain casing must share one height-check series, got %d", got)
	}
	if got := sink.counters["thornode.solvency.report_stale{chain=TRON}"]; got != 3 {
		t.Fatalf("existing stale-report counter must still record all three reports, got %v", got)
	}
}

type HandlerSolvencyHelpersSuite struct{}

var _ = Suite(&HandlerSolvencyHelpersSuite{})

func (s *HandlerSolvencyHelpersSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

// setupSolvencyHelpersTest creates common test fixtures for solvency helper tests.
func setupSolvencyHelpersTest(c *C) (cosmos.Context, Manager, NodeAccounts, Vault) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1024)

	// 4 active nodes, consensus needs 2 (1/3 of 4 = 1.33, rounded up = 2)
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
	}
	for _, na := range nas {
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	}

	vault := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), []string{
		common.ETHChain.String(),
	}, nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Set up ETH pool: 1000 ETH = 10000 RUNE (1 ETH = 10 RUNE)
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	ethPool.BalanceRune = cosmos.NewUint(10000 * common.One)
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	// Set RUNE price ($5/RUNE)
	mgr.Keeper().SetMimir(ctx, "DollarsPerRune", 5_00000000)

	return ctx, mgr, nas, vault
}

// bringVoterToConsensus signs a solvency voter with enough nodes to reach consensus.
func bringVoterToConsensus(voter *keeper.SolvencyVoter, nas NodeAccounts) {
	voter.Sign(nas[0].NodeAddress)
	voter.Sign(nas[1].NodeAddress)
}

func setupTRONSolvencyHelpersTest(c *C) (cosmos.Context, Manager, NodeAccounts, Vault, common.Coins) {
	ctx, mgr, nas, _ := setupSolvencyHelpersTest(c)

	usdt, err := common.NewAsset("TRON.USDT-TR7NHQJEKQXGTCI8Q8ZY4PL8OTSZGJLJ6T")
	c.Assert(err, IsNil)
	coins := common.NewCoins(
		common.NewCoin(common.TRXAsset, cosmos.NewUint(1_000_000_000)),
		common.NewCoin(usdt, cosmos.NewUint(10_000_000_000)),
	)
	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), []string{
		common.TRONChain.String(),
	}, nil)
	vault.AddFunds(coins)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, common.TRONChain, ctx.BlockHeight()), IsNil)

	return ctx, mgr, nas, vault, coins
}

func assertSlashPoints(c *C, ctx cosmos.Context, mgr Manager, nas NodeAccounts, expected int64) {
	for _, na := range nas {
		points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
		c.Assert(err, IsNil)
		c.Assert(points, Equals, expected)
	}
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_DuplicateSigner(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	// First sign succeeds
	err := processSolvencyAttestation(ctx, mgr, &voter, nas[0].NodeAddress, nas, sol, true)
	c.Assert(err, IsNil)

	// Duplicate sign with shouldSlashForDuplicate=true should slash but not error
	err = processSolvencyAttestation(ctx, mgr, &voter, nas[0].NodeAddress, nas, sol, true)
	c.Assert(err, IsNil)
	pts, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nas[0].NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(pts > 1, Equals, true) // extra slash for duplicate
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_TRONFragmentationDoesNotSlash(c *C) {
	ctx, mgr, nas, vault, coins := setupTRONSolvencyHelpersTest(c)
	const initialSlashPoints = int64(100)
	for _, na := range nas {
		mgr.Keeper().SetNodeAccountSlashPoints(ctx, na.NodeAddress, initialSlashPoints)
	}

	// A different current-state TRC-20 balance produces a competing voter for
	// the same canonical height. Its signer must not retain a pre-consensus
	// observation penalty.
	fragmentedCoins := coins.Copy()
	fragmentedCoins[1].Amount = fragmentedCoins[1].Amount.Sub(cosmos.OneUint())
	fragmentedVoter := NewSolvencyVoter(
		GetRandomTxHash(), common.TRONChain, vault.PubKey, fragmentedCoins, ctx.BlockHeight(),
	)
	fragmented := &common.Solvency{
		Id: fragmentedVoter.Id, Chain: fragmentedVoter.Chain, PubKey: fragmentedVoter.PubKey,
		Coins: fragmentedVoter.Coins, Height: fragmentedVoter.Height,
	}
	c.Assert(processSolvencyAttestation(
		ctx, mgr, &fragmentedVoter, nas[0].NodeAddress, nas, fragmented, false,
	), IsNil)
	assertSlashPoints(c, ctx, mgr, nas, initialSlashPoints)

	// A second payload reaches the 1/3 solvency threshold. Neither its signers
	// nor the fragmented signer and other non-signers receive slash points.
	winnerVoter := NewSolvencyVoter(
		GetRandomTxHash(), common.TRONChain, vault.PubKey, coins, ctx.BlockHeight(),
	)
	winner := &common.Solvency{
		Id: winnerVoter.Id, Chain: winnerVoter.Chain, PubKey: winnerVoter.PubKey,
		Coins: winnerVoter.Coins, Height: winnerVoter.Height,
	}
	c.Assert(processSolvencyAttestation(
		ctx, mgr, &winnerVoter, nas[1].NodeAddress, nas, winner, false,
	), IsNil)
	c.Assert(processSolvencyAttestation(
		ctx, mgr, &winnerVoter, nas[2].NodeAddress, nas, winner, false,
	), IsNil)
	c.Assert(winnerVoter.ConsensusBlockHeight, Equals, ctx.BlockHeight())
	assertSlashPoints(c, ctx, mgr, nas, initialSlashPoints)

	flex := mgr.Keeper().GetConfigInt64(ctx, constants.ObservationDelayFlexibility)
	entries, err := mgr.Keeper().PopObservationTallies(ctx, ctx.BlockHeight()+flex)
	c.Assert(err, IsNil)
	c.Assert(entries, HasLen, 0)
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_TRONStaleConsensusDoesNotSlashOrAct(c *C) {
	ctx, mgr, nas, vault, coins := setupTRONSolvencyHelpersTest(c)
	const initialSlashPoints = int64(100)
	for _, na := range nas {
		mgr.Keeper().SetNodeAccountSlashPoints(ctx, na.NodeAddress, initialSlashPoints)
	}

	// Make the report both insolvent and stale. The consensus is recorded, but
	// it must neither change slash accounting nor halt TRON.
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, common.TRONChain, ctx.BlockHeight()+1), IsNil)
	reportedCoins := coins.Copy()
	reportedCoins[1].Amount = reportedCoins[1].Amount.QuoUint64(2)
	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.TRONChain, vault.PubKey, reportedCoins, ctx.BlockHeight(),
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	c.Assert(processSolvencyAttestation(ctx, mgr, &voter, nas[0].NodeAddress, nas, sol, false), IsNil)
	c.Assert(processSolvencyAttestation(ctx, mgr, &voter, nas[1].NodeAddress, nas, sol, false), IsNil)
	c.Assert(voter.ConsensusBlockHeight, Equals, ctx.BlockHeight())
	assertSlashPoints(c, ctx, mgr, nas, initialSlashPoints)

	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltTRONChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(-1))
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_TRONLateSignerDoesNotReceivePhantomRefund(c *C) {
	ctx, mgr, nas, vault, coins := setupTRONSolvencyHelpersTest(c)
	const initialSlashPoints = int64(100)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, nas[2].NodeAddress, initialSlashPoints)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.TRONChain, vault.PubKey, coins, ctx.BlockHeight(),
	)
	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = ctx.BlockHeight()
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	c.Assert(processSolvencyAttestation(
		ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false,
	), IsNil)
	points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nas[2].NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(points, Equals, initialSlashPoints)
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_TRONDuplicateStillSlashes(c *C) {
	ctx, mgr, nas, vault, coins := setupTRONSolvencyHelpersTest(c)
	const initialSlashPoints = int64(100)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, nas[0].NodeAddress, initialSlashPoints)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.TRONChain, vault.PubKey, coins, ctx.BlockHeight(),
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	c.Assert(processSolvencyAttestation(ctx, mgr, &voter, nas[0].NodeAddress, nas, sol, true), IsNil)
	c.Assert(processSolvencyAttestation(ctx, mgr, &voter, nas[0].NodeAddress, nas, sol, true), IsNil)
	points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nas[0].NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(points, Equals, initialSlashPoints+1)
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_StopSolvencyCheckMimir(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Set StopSolvencyCheck to a height before current block (active stop)
	mgr.Keeper().SetMimir(ctx, "StopSolvencyCheck", ctx.BlockHeight()-1)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	// Bring to consensus
	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Even though vault is insolvent, StopSolvencyCheck prevents halting
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(-1))

	flex := mgr.Keeper().GetConfigInt64(ctx, constants.ObservationDelayFlexibility)
	entries, err := mgr.Keeper().PopObservationTallies(ctx, ctx.BlockHeight()+flex)
	c.Assert(err, IsNil)
	c.Assert(entries, HasLen, 1)
	c.Assert(entries[0].Kind, Equals, observationTallyKindSolvency)
	c.Assert(entries[0].Chain, Equals, common.ETHChain)
	c.Assert(entries[0].Ref, DeepEquals, []string{voter.Id.String()})
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_StopSolvencyCheckChainMimir(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Set chain-specific StopSolvencyCheck
	mgr.Keeper().SetMimir(ctx, "StopSolvencyCheckETH", ctx.BlockHeight()-1)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(-1))
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_HaltChainAlreadySet(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Set SolvencyHaltETHChain to current block height (already halted)
	mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", ctx.BlockHeight())

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Halt value should not have changed
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, ctx.BlockHeight())
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_HaltChainManuallyHalted(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Set SolvencyHaltETHChain to 1 (indefinite manual halt)
	mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", 1)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Should stay at 1 (manually halted)
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(1))
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_SolvencyHeightBelowLastChainHeight(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Set last chain height higher than solvency msg height
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, common.ETHChain, 2000), IsNil)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024, // lower than lastChainHeight=2000
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Should not halt because solvency height is stale
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(-1))
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_GetVaultError(c *C) {
	ctx, mgr, nas, _ := setupSolvencyHelpersTest(c)

	// Use a pubkey that has no vault stored
	unknownPK := GetRandomPubKey()
	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, unknownPK,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*fail to get vault.*")
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_AutoUnhalt(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Chain was previously halted at block 500 (earlier than current 1024)
	mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", 500)

	// Vault has 1000 ETH, wallet also reports 1000 ETH → solvent
	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Chain should be auto-unhalted (mimir set to 0)
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(0))
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_FlagsVaultInsolvent(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Vault holds 1000 ETH but wallet only reports 100 ETH → insolvent.
	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Chain halted and the specific vault flagged insolvent for its chain.
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, ctx.BlockHeight())

	flag, err := mgr.Keeper().GetMimir(ctx, solvencyHaltVaultKey(vault.PubKey, common.ETHChain))
	c.Assert(err, IsNil)
	c.Assert(flag, Equals, ctx.BlockHeight())

	insolvent, err := isVaultInsolvent(ctx, mgr.Keeper(), vault)
	c.Assert(err, IsNil)
	c.Assert(insolvent, Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_ClearsVaultInsolventFlag(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Chain previously halted and vault previously flagged insolvent.
	mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", 500)
	flagVaultInsolvent(ctx.WithBlockHeight(500), mgr.Keeper(), vault.PubKey, common.ETHChain)

	// Vault has 1000 ETH and wallet also reports 1000 ETH → solvent again.
	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Both the chain halt and the vault flag are cleared.
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, int64(0))

	insolvent, err := isVaultInsolvent(ctx, mgr.Keeper(), vault)
	c.Assert(err, IsNil)
	c.Assert(insolvent, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_ClearsVaultInsolventFlagWithoutUnhaltingChain(c *C) {
	testCases := []struct {
		name             string
		halt             int64
		haltCurrentBlock bool
	}{
		{name: "halted this block", haltCurrentBlock: true},
		{name: "manual halt", halt: 1},
	}

	for _, tc := range testCases {
		ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

		expectedHalt := tc.halt
		if tc.haltCurrentBlock {
			expectedHalt = ctx.BlockHeight()
		}
		mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", expectedHalt)
		flagVaultInsolvent(ctx.WithBlockHeight(500), mgr.Keeper(), vault.PubKey, common.ETHChain)

		// Vault has 1000 ETH and wallet also reports 1000 ETH -> solvent again.
		voter := NewSolvencyVoter(
			GetRandomTxHash(), common.ETHChain, vault.PubKey,
			common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))),
			1024,
		)
		sol := &common.Solvency{
			Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
		}

		bringVoterToConsensus(&voter, nas)
		voter.ConsensusBlockHeight = 0

		err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
		c.Assert(err, IsNil, Commentf(tc.name))

		// Chain-wide halt state is preserved, but the recovered vault's exclusion
		// flag is cleared so unrelated healthy chains can elect it again.
		halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
		c.Assert(err, IsNil, Commentf(tc.name))
		c.Assert(halt, Equals, expectedHalt, Commentf(tc.name))

		flag, err := mgr.Keeper().GetMimir(ctx, solvencyHaltVaultKey(vault.PubKey, common.ETHChain))
		c.Assert(err, IsNil, Commentf(tc.name))
		c.Assert(flag, Equals, int64(0), Commentf(tc.name))

		insolvent, err := isVaultInsolvent(ctx, mgr.Keeper(), vault)
		c.Assert(err, IsNil, Commentf(tc.name))
		c.Assert(insolvent, Equals, false, Commentf(tc.name))
	}
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_DoesNotClearVaultInsolventFlagForStaleHeight(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", ctx.BlockHeight())
	flagVaultInsolvent(ctx.WithBlockHeight(500), mgr.Keeper(), vault.PubKey, common.ETHChain)
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, common.ETHChain, 2000), IsNil)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	flag, err := mgr.Keeper().GetMimir(ctx, solvencyHaltVaultKey(vault.PubKey, common.ETHChain))
	c.Assert(err, IsNil)
	c.Assert(flag, Equals, int64(500))

	insolvent, err := isVaultInsolvent(ctx, mgr.Keeper(), vault)
	c.Assert(err, IsNil)
	c.Assert(insolvent, Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_EmptyCoin(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	// Add an empty coin to vault
	vault.Coins = append(vault.Coins, common.NewCoin(common.BTCAsset, cosmos.ZeroUint()))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	walletCoins := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One)))

	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.ETHChain)
	c.Assert(result, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_WalletCoinEmpty(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	// Wallet has no ETH at all → gap = vault amount
	walletCoins := common.Coins{}

	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.ETHChain)
	c.Assert(result, Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_PendingOutboundCoversWalletGap(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	prevHeight := ctx.BlockHeight() - 1
	txOut := NewTxOut(prevHeight)
	txOut.TxArray = []TxOutItem{
		{
			Chain:       common.ETHChain,
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(20*common.One)),
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	// Vault has 1000 ETH, wallet has 980 ETH, and the remaining 20 ETH is
	// already committed to an outstanding outbound.
	walletCoins := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(980*common.One)))

	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.ETHChain)
	c.Assert(result, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_PendingOutboundCanExceedWalletBalance(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	prevHeight := ctx.BlockHeight() - 1
	txOut := NewTxOut(prevHeight)
	txOut.TxArray = []TxOutItem{
		{
			Chain:       common.ETHChain,
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One)),
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	// The wallet can be empty without being insolvent if the full vault amount
	// is already represented by pending outbounds.
	walletCoins := common.Coins{}

	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.ETHChain)
	c.Assert(result, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_PendingOutboundExceedsVaultBalance(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	prevHeight := ctx.BlockHeight() - 1
	txOut := NewTxOut(prevHeight)
	txOut.TxArray = []TxOutItem{
		{
			Chain:       common.ETHChain,
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(1500*common.One)),
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	// Over-scheduled pending outbounds are handled by outbound recovery and
	// should not make solvency fall back to checking the full vault balance.
	walletCoins := common.Coins{}

	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.ETHChain)
	c.Assert(result, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_GapWithinPermittedRange(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	// Vault has 1000 ETH, wallet has 999.99 ETH → small gap
	walletCoins := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(999_99000000)))

	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.ETHChain)
	c.Assert(result, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_NoPoolLiquidity(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	// Replace ETH pool with empty pool
	ethPool := NewPool()
	ethPool.Asset = common.ETHAsset
	ethPool.BalanceAsset = cosmos.ZeroUint()
	ethPool.BalanceRune = cosmos.ZeroUint()
	ethPool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

	// Wallet has less ETH than vault
	walletCoins := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(500*common.One)))

	// Pool has no liquidity → gap can't be valued in RUNE → treat as insolvent (conservative)
	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.ETHChain)
	c.Assert(result, Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_WalletMoreThanVault(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	// Wallet has more than vault → no gap
	walletCoins := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(2000*common.One)))

	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.ETHChain)
	c.Assert(result, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestInsolvencyCheck_DifferentChainCoinSkipped(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	// Check BTC chain insolvency - vault has no BTC coins
	walletCoins := common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)))

	result := insolvencyCheck(ctx, mgr, vault, walletCoins, common.BTCChain)
	c.Assert(result, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestDeductVaultBlockPendingOutbound_NoMatchingVault(c *C) {
	ctx, mgr, _, _ := setupSolvencyHelpersTest(c)

	vault := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), []string{
		common.ETHChain.String(),
	}, nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))))

	block := &TxOut{
		Height: 1024,
		TxArray: []TxOutItem{
			{
				Chain:       common.ETHChain,
				VaultPubKey: GetRandomPubKey(), // different vault
				Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
			},
		},
	}

	signingPeriod := mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	result, _ := deductVaultBlockPendingOutbound(ctx, mgr, vault, block, make(map[string]cosmos.Uint), common.ETHChain, signingPeriod)
	// Balance should be unchanged since VaultPubKey doesn't match
	ethCoin := result.Coins.GetCoin(common.ETHAsset)
	c.Assert(ethCoin.Amount.Equal(cosmos.NewUint(1000*common.One)), Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestDeductVaultBlockPendingOutbound_AlreadySent(c *C) {
	ctx, mgr, _, _ := setupSolvencyHelpersTest(c)

	vault := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), []string{
		common.ETHChain.String(),
	}, nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))))

	block := &TxOut{
		Height: 1024,
		TxArray: []TxOutItem{
			{
				Chain:       common.ETHChain,
				VaultPubKey: vault.PubKey,
				Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
				OutHash:     GetRandomTxHash(), // already sent
			},
		},
	}

	signingPeriod := mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	result, _ := deductVaultBlockPendingOutbound(ctx, mgr, vault, block, make(map[string]cosmos.Uint), common.ETHChain, signingPeriod)
	// Balance should be unchanged since tx already has OutHash
	ethCoin := result.Coins.GetCoin(common.ETHAsset)
	c.Assert(ethCoin.Amount.Equal(cosmos.NewUint(1000*common.One)), Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestDeductVaultBlockPendingOutbound_WithGas(c *C) {
	ctx, mgr, _, _ := setupSolvencyHelpersTest(c)

	vault := NewVault(1024, ActiveVault, AsgardVault, GetRandomPubKey(), []string{
		common.ETHChain.String(),
	}, nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))))

	block := &TxOut{
		Height: 1024,
		TxArray: []TxOutItem{
			{
				Chain:       common.ETHChain,
				VaultPubKey: vault.PubKey,
				Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
				MaxGas: common.Gas{
					common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One)),
				},
			},
		},
	}

	signingPeriod := mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	result, _ := deductVaultBlockPendingOutbound(ctx, mgr, vault, block, make(map[string]cosmos.Uint), common.ETHChain, signingPeriod)
	// Should deduct both the coin amount and gas
	ethCoin := result.Coins.GetCoin(common.ETHAsset)
	expected := cosmos.NewUint(899 * common.One) // 1000 - 100 - 1
	c.Assert(ethCoin.Amount.Equal(expected), Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestExcludePendingOutboundFromVault_Success(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)

	// Set a pending outbound at a height within the signing period (before current block)
	prevHeight := ctx.BlockHeight() - 1
	txOut := NewTxOut(prevHeight)
	txOut.TxArray = []TxOutItem{
		{
			Chain:       common.ETHChain,
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One)),
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	adjusted, _, err := excludePendingOutboundFromVault(ctx, mgr, vault, common.ETHChain)
	c.Assert(err, IsNil)
	ethCoin := adjusted.Coins.GetCoin(common.ETHAsset)
	expected := cosmos.NewUint(950 * common.One) // 1000 - 50
	c.Assert(ethCoin.Amount.Equal(expected), Equals, true)

	// Original vault coins should not be mutated
	originalETH := vault.Coins.GetCoin(common.ETHAsset)
	c.Assert(originalETH.Amount.Equal(cosmos.NewUint(1000*common.One)), Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestExcludePendingOutboundFromVaultScopesToReportedChain(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(1000*common.One))))

	prevHeight := ctx.BlockHeight() - 1
	txOut := NewTxOut(prevHeight)
	txOut.TxArray = []TxOutItem{
		{
			Chain:       common.BTCChain,
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(50*common.One)),
		},
		{
			Chain:       common.ETHChain,
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(75*common.One)),
		},
	}
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)

	adjusted, pending, err := excludePendingOutboundFromVault(ctx, mgr, vault, common.BTCChain)
	c.Assert(err, IsNil)
	c.Assert(adjusted.Coins.GetCoin(common.BTCAsset).Amount.Equal(cosmos.NewUint(950*common.One)), Equals, true)
	c.Assert(adjusted.Coins.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(1000*common.One)), Equals, true)
	c.Assert(pending[common.BTCAsset.String()].Equal(cosmos.NewUint(50*common.One)), Equals, true)
	_, hasETHPending := pending[common.ETHAsset.String()]
	c.Assert(hasETHPending, Equals, false)
}

func (s *HandlerSolvencyHelpersSuite) TestExcludePendingOutboundFromVaultRetainsExtendedEVMClasses(c *C) {
	ctx, mgr, _, vault := setupSolvencyHelpersTest(c)
	defaultSigningPeriod := mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	overrideSigningPeriod := defaultSigningPeriod * 2
	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), overrideSigningPeriod)
	signingPeriod := mgr.Keeper().GetConfigInt64(ctx, constants.SigningTransactionPeriod)
	c.Assert(signingPeriod, Equals, overrideSigningPeriod)
	c.Assert(signingPeriod > 0, Equals, true)
	ctx = ctx.WithBlockHeight(signingPeriod*4 + 10)
	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)
	vault.AddFunds(common.NewCoins(common.NewCoin(usdc, cosmos.NewUint(1000*common.One))))

	addTxOut := func(height int64, items ...TxOutItem) {
		txOut := NewTxOut(height)
		txOut.TxArray = items
		c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)
	}
	justPastStandard := ctx.BlockHeight() - signingPeriod - 1
	justInsideExtended := ctx.BlockHeight() - signingPeriod*3 + 1
	addTxOut(justPastStandard,
		TxOutItem{Chain: common.ETHChain, VaultPubKey: vault.PubKey, Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One)), Aggregator: "0xaggregator"},
		TxOutItem{Chain: common.ETHChain, VaultPubKey: vault.PubKey, Coin: common.NewCoin(usdc, cosmos.NewUint(70*common.One))},
		TxOutItem{Chain: common.ETHChain, VaultPubKey: vault.PubKey, Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One))},
	)
	addTxOut(justInsideExtended,
		TxOutItem{Chain: common.ETHChain, VaultPubKey: vault.PubKey, Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One)), Aggregator: "0xaggregator"},
		TxOutItem{Chain: common.ETHChain, VaultPubKey: vault.PubKey, Coin: common.NewCoin(usdc, cosmos.NewUint(80*common.One))},
	)

	adjusted, pending, err := excludePendingOutboundFromVault(ctx, mgr, vault, common.ETHChain)
	c.Assert(err, IsNil)
	c.Assert(adjusted.Coins.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(900*common.One)), Equals, true)
	c.Assert(adjusted.Coins.GetCoin(usdc).Amount.Equal(cosmos.NewUint(850*common.One)), Equals, true)
	c.Assert(pending[common.ETHAsset.String()].Equal(cosmos.NewUint(100*common.One)), Equals, true)
	c.Assert(pending[usdc.String()].Equal(cosmos.NewUint(150*common.One)), Equals, true)
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_InsolventButAlreadyHalted(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// Chain was previously halted at block 500 (haltChain > 0, but < blockHeight)
	mgr.Keeper().SetMimir(ctx, "SolvencyHaltETHChain", 500)

	// Vault has 1000 ETH, wallet only has 100 ETH → insolvent
	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Halt value should be refreshed to the current block height to prevent
	// a different solvent vault from unhalting the chain prematurely.
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, ctx.BlockHeight())
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_InsolventAndUnhalted(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	// No halt set (default -1)

	// Vault has 1000 ETH, wallet only has 100 ETH → insolvent
	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 0

	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Should halt the chain
	halt, err := mgr.Keeper().GetMimir(ctx, "SolvencyHaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(halt, Equals, ctx.BlockHeight())
}

func (s *HandlerSolvencyHelpersSuite) TestProcessSolvencyAttestation_ConsensusAlreadyReached(c *C) {
	ctx, mgr, nas, vault := setupSolvencyHelpersTest(c)

	voter := NewSolvencyVoter(
		GetRandomTxHash(), common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))),
		1024,
	)
	sol := &common.Solvency{
		Id: voter.Id, Chain: voter.Chain, PubKey: voter.PubKey, Coins: voter.Coins, Height: voter.Height,
	}

	// Mark consensus already reached
	bringVoterToConsensus(&voter, nas)
	voter.ConsensusBlockHeight = 100

	// Late observer within flex period should get slash points decremented
	err := processSolvencyAttestation(ctx, mgr, &voter, nas[2].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)

	// Late observer beyond flex period
	ctx = ctx.WithBlockHeight(voter.ConsensusBlockHeight + 100)
	err = processSolvencyAttestation(ctx, mgr, &voter, nas[3].NodeAddress, nas, sol, false)
	c.Assert(err, IsNil)
}
