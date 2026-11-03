package thorchain

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/btcsuite/btcutil/bech32"
	"gitlab.com/thorchain/thornode/v3/cmd"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tcysmartcontract"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type GenesisTestSuite struct{}

var _ = Suite(&GenesisTestSuite{})

var genesisFixtureBech32 = regexp.MustCompile(`thor[a-z]*1[0-9a-z]+`)

func normalizeGenesisFixtureBech32(input string) string {
	return genesisFixtureBech32.ReplaceAllStringFunc(input, func(raw string) string {
		_, data, err := bech32.DecodeNoLimit(raw)
		if err != nil {
			return raw
		}

		targetPrefix := cmd.Bech32PrefixAccAddr
		switch {
		case strings.HasPrefix(raw, "thorcpub1"):
			targetPrefix = cmd.Bech32PrefixConsPub
		case strings.HasPrefix(raw, "thorpub1"):
			targetPrefix = cmd.Bech32PrefixAccPub
		case strings.HasPrefix(raw, "thorc1"):
			targetPrefix = cmd.Bech32PrefixConsAddr
		}

		normalized, err := bech32.Encode(targetPrefix, data)
		if err != nil {
			return raw
		}
		return normalized
	})
}

func (GenesisTestSuite) TestGenesis(c *C) {
	SetupConfigForTest()
	genesisState := DefaultGenesisState()
	c.Assert(ValidateGenesis(genesisState), IsNil)
	ctx, mgr := setupManagerForTest(c)
	gs := ExportGenesis(ctx, mgr.Keeper())
	c.Assert(ValidateGenesis(gs), IsNil)
	content, err := os.ReadFile("../../test/fixtures/genesis/genesis.json")
	c.Assert(err, IsNil)
	c.Assert(content, NotNil)
	normalizedContent := normalizeGenesisFixtureBech32(string(content))
	ctx, mgr = setupManagerForTest(c)
	var state GenesisState
	c.Assert(ModuleCdc.UnmarshalJSON([]byte(normalizedContent), &state), IsNil)
	result := InitGenesis(ctx, mgr.Keeper(), state)
	c.Assert(result, NotNil)
	gs1 := ExportGenesis(ctx, mgr.Keeper())
	c.Assert(len(gs1.Pools) > 0, Equals, true)
}

func (GenesisTestSuite) TestGenesisPOLReserveDepositRoundTrip(c *C) {
	SetupConfigForTest()

	srcCtx, srcMgr := setupManagerForTest(c)
	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)

	btcDeposit := NewPOLReserveDeposit(common.BTCAsset)
	btcDeposit.RuneDeposited = cosmos.NewUint(12345 * common.One)
	c.Assert(srcMgr.Keeper().SetPOLReserveDeposit(srcCtx, btcDeposit), IsNil)

	usdcDeposit := NewPOLReserveDeposit(usdc)
	usdcDeposit.RuneDeposited = cosmos.NewUint(678 * common.One)
	c.Assert(srcMgr.Keeper().SetPOLReserveDeposit(srcCtx, usdcDeposit), IsNil)

	exported := ExportGenesis(srcCtx, srcMgr.Keeper())
	found := map[string]cosmos.Uint{}
	for _, prd := range exported.PolReserveDeposits {
		found[prd.Asset.String()] = prd.RuneDeposited
	}
	c.Check(found[common.BTCAsset.String()].String(), Equals, btcDeposit.RuneDeposited.String())
	c.Check(found[usdc.String()].String(), Equals, usdcDeposit.RuneDeposited.String())

	dstCtx, dstMgr := setupManagerForTest(c)
	InitGenesis(dstCtx, dstMgr.Keeper(), exported)

	gotBTC, err := dstMgr.Keeper().GetPOLReserveDeposit(dstCtx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(gotBTC.Asset.Equals(common.BTCAsset), Equals, true)
	c.Check(gotBTC.RuneDeposited.String(), Equals, btcDeposit.RuneDeposited.String())

	gotUSDC, err := dstMgr.Keeper().GetPOLReserveDeposit(dstCtx, usdc)
	c.Assert(err, IsNil)
	c.Check(gotUSDC.Asset.Equals(usdc), Equals, true)
	c.Check(gotUSDC.RuneDeposited.String(), Equals, usdcDeposit.RuneDeposited.String())
}

func (GenesisTestSuite) TestGenesisInactiveAsgardDeficitRoundTrip(c *C) {
	SetupConfigForTest()

	srcCtx, srcMgr := setupManagerForTest(c)
	deficitVault := GetRandomVault()
	deficitVault.Status = InactiveVault
	deficitVault.StatusSince = srcCtx.BlockHeight()
	deficitVault.Coins = common.Coins{}
	deficitVault.Deficit = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(12345)))
	c.Assert(srcMgr.Keeper().SetVault(srcCtx, deficitVault), IsNil)
	c.Assert(srcMgr.Keeper().RemoveFromAsgardIndex(srcCtx, deficitVault.PubKey), IsNil)
	initDeficitVault := GetRandomVault()
	initDeficitVault.Status = InitVault
	initDeficitVault.StatusSince = srcCtx.BlockHeight()
	initDeficitVault.Coins = common.Coins{}
	initDeficitVault.Deficit = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(67890)))
	c.Assert(srcMgr.Keeper().SetVault(srcCtx, initDeficitVault), IsNil)
	c.Assert(srcMgr.Keeper().RemoveFromAsgardIndex(srcCtx, initDeficitVault.PubKey), IsNil)

	abandonedVault := GetRandomVault()
	abandonedVault.Status = InactiveVault
	abandonedVault.Coins = common.Coins{}
	abandonedVault.Deficit = common.Coins{}
	c.Assert(srcMgr.Keeper().SetVault(srcCtx, abandonedVault), IsNil)
	c.Assert(srcMgr.Keeper().RemoveFromAsgardIndex(srcCtx, abandonedVault.PubKey), IsNil)

	exported := ExportGenesis(srcCtx, srcMgr.Keeper())
	findVault := func(vaults Vaults, pubKey common.PubKey) (Vault, bool) {
		for _, vault := range vaults {
			if vault.PubKey.Equals(pubKey) {
				return vault, true
			}
		}
		return Vault{}, false
	}

	exportedDeficitVault, found := findVault(exported.Vaults, deficitVault.PubKey)
	c.Assert(found, Equals, true)
	c.Check(exportedDeficitVault.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.NewUint(12345)), Equals, true)
	exportedInitDeficitVault, found := findVault(exported.Vaults, initDeficitVault.PubKey)
	c.Assert(found, Equals, true)
	c.Check(exportedInitDeficitVault.GetDeficit(common.BTCAsset).Amount.Equal(cosmos.NewUint(67890)), Equals, true)
	_, found = findVault(exported.Vaults, abandonedVault.PubKey)
	c.Check(found, Equals, false)

	dstCtx, dstMgr := setupManagerForTest(c)
	InitGenesis(dstCtx, dstMgr.Keeper(), exported)

	restored, err := dstMgr.Keeper().GetVault(dstCtx, deficitVault.PubKey)
	c.Assert(err, IsNil)
	c.Check(restored.Status, Equals, InactiveVault)
	c.Check(restored.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.NewUint(12345)), Equals, true)
	restoredInit, err := dstMgr.Keeper().GetVault(dstCtx, initDeficitVault.PubKey)
	c.Assert(err, IsNil)
	c.Check(restoredInit.Status, Equals, InitVault)
	c.Check(restoredInit.GetDeficit(common.BTCAsset).Amount.Equal(cosmos.NewUint(67890)), Equals, true)

	reExported := ExportGenesis(dstCtx, dstMgr.Keeper())
	reExportedDeficitVault, found := findVault(reExported.Vaults, deficitVault.PubKey)
	c.Assert(found, Equals, true)
	c.Check(reExportedDeficitVault.GetDeficit(common.ETHAsset).Amount.Equal(cosmos.NewUint(12345)), Equals, true)
	reExportedInitDeficitVault, found := findVault(reExported.Vaults, initDeficitVault.PubKey)
	c.Assert(found, Equals, true)
	c.Check(reExportedInitDeficitVault.GetDeficit(common.BTCAsset).Amount.Equal(cosmos.NewUint(67890)), Equals, true)
}

// TestGenesisStableReserveCreditRoundTrip seeds per-contributor stable reserve
// credits, exports them, and imports into a fresh keeper. Without genesis
// wiring the credits are lost on a state export/import and every prior
// contributor's STABLE- withdrawal would fail with "insufficient credit".
func (GenesisTestSuite) TestGenesisStableReserveCreditRoundTrip(c *C) {
	SetupConfigForTest()

	srcCtx, srcMgr := setupManagerForTest(c)
	ethAddr, err := common.NewAddress("0x3021c479f7f8c9f1d5c7d8523ba5e22c0bcb5430")
	c.Assert(err, IsNil)
	btcAddr, err := common.NewAddress("bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq")
	c.Assert(err, IsNil)
	// A case-sensitive base58check (TRON) address: uppercasing it — as the store
	// key does — corrupts the checksum, so it can only survive export/import if the
	// original case is preserved in the value.
	tronAddr, err := common.NewAddress("TWcDDx1Q6QEoBrJi9qehtZnD4vcXXuVLer")
	c.Assert(err, IsNil)

	c.Assert(srcMgr.Keeper().SetStableReserveCredit(srcCtx, common.ETHAsset, ethAddr, cosmos.NewUint(1234*common.One)), IsNil)
	c.Assert(srcMgr.Keeper().SetStableReserveCredit(srcCtx, common.BTCAsset, btcAddr, cosmos.NewUint(5678)), IsNil)
	c.Assert(srcMgr.Keeper().SetStableReserveCredit(srcCtx, common.TRXAsset, tronAddr, cosmos.NewUint(9012)), IsNil)

	exported := ExportGenesis(srcCtx, srcMgr.Keeper())
	c.Assert(len(exported.StableReserveCredits), Equals, 3)

	// Exercise the binary proto round-trip explicitly so the marshal/unmarshal
	// path for the new field is covered, not just the in-memory export.
	bz, err := exported.Marshal()
	c.Assert(err, IsNil)
	var decoded GenesisState
	c.Assert(decoded.Unmarshal(bz), IsNil)
	c.Assert(len(decoded.StableReserveCredits), Equals, 3)

	dstCtx, dstMgr := setupManagerForTest(c)
	InitGenesis(dstCtx, dstMgr.Keeper(), decoded)

	gotETH, err := dstMgr.Keeper().GetStableReserveCredit(dstCtx, common.ETHAsset, ethAddr)
	c.Assert(err, IsNil)
	c.Check(gotETH.String(), Equals, cosmos.NewUint(1234*common.One).String())

	gotBTC, err := dstMgr.Keeper().GetStableReserveCredit(dstCtx, common.BTCAsset, btcAddr)
	c.Assert(err, IsNil)
	c.Check(gotBTC.String(), Equals, cosmos.NewUint(5678).String())

	// The base58check address must survive with its original case intact so the
	// credit is still keyed to (and withdrawable by) the real contributor.
	c.Check(exportedAddress(exported, common.TRXAsset), Equals, "TWcDDx1Q6QEoBrJi9qehtZnD4vcXXuVLer")
	gotTRON, err := dstMgr.Keeper().GetStableReserveCredit(dstCtx, common.TRXAsset, tronAddr)
	c.Assert(err, IsNil)
	c.Check(gotTRON.String(), Equals, cosmos.NewUint(9012).String())
}

// exportedAddress returns the exported stable-reserve credit address for a given
// layer-1 asset, or "" if absent.
func exportedAddress(g GenesisState, asset common.Asset) string {
	for _, c := range g.StableReserveCredits {
		if a, err := common.NewAsset(c.Asset); err == nil && a.Equals(asset) {
			return c.Address
		}
	}
	return ""
}

// TestGenesisDynamicFeeRoundtrip seeds a DynamicFeeRecord, accumulator, and
// last-observed-epoch-blocks value into one keeper, exports the state,
// imports into a fresh keeper, and re-exports. It then asserts the two
// exports are equivalent. Without wiring in ExportGenesis / initGenesis
// the re-export would silently drop the dynamic-fee state — leaving the
// controller blind to its prior direction across chain restarts and
// losing in-flight pre-seal volume.
func (GenesisTestSuite) TestGenesisDynamicFeeRoundtrip(c *C) {
	SetupConfigForTest()
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	rec := types.DynamicFeeRecord{
		Thorname:        "bentley",
		PairAssetA:      "BTC.BTC",
		PairAssetB:      "THOR.RUNE",
		DynamicBps:      7,
		LastActiveEpoch: 42,
		History: []types.DynamicFeeEpochRecord{
			{Epoch: 41, VolumeTor: cosmos.NewUint(100), FeesTor: cosmos.NewUint(10), BpsAtClose: 6},
			{Epoch: 42, VolumeTor: cosmos.NewUint(200), FeesTor: cosmos.NewUint(20), BpsAtClose: 7},
		},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, rec), IsNil)

	acc := types.DynamicFeeAccumulator{
		Thorname:   "bentley",
		PairAssetA: "BTC.BTC",
		PairAssetB: "THOR.RUNE",
		VolumeTor:  cosmos.NewUint(333),
		FeesTor:    cosmos.NewUint(33),
	}
	c.Assert(k.SetDynamicFeeAccumulator(ctx, acc), IsNil)
	k.SetLastObservedDynamicFeeEpochBlocks(ctx, 1337)

	exported := ExportGenesis(ctx, k)
	c.Check(len(exported.DynamicFeeRecords), Equals, 1)
	c.Check(len(exported.DynamicFeeAccumulators), Equals, 1)
	c.Check(exported.LastObservedDynamicFeeEpochBlocks, Equals, int64(1337))
	c.Check(exported.DynamicFeeRecords[0].DynamicBps, Equals, uint64(7))
	c.Check(len(exported.DynamicFeeRecords[0].History), Equals, 2)

	// Fresh keeper → initGenesis from exported → re-export → deep compare.
	ctx2, mgr2 := setupManagerForTest(c)
	k2 := mgr2.Keeper()
	InitGenesis(ctx2, k2, exported)

	reimported, err := k2.GetDynamicFeeRecord(ctx2, "bentley", "BTC.BTC", "THOR.RUNE")
	c.Assert(err, IsNil)
	c.Check(reimported.DynamicBps, Equals, uint64(7))
	c.Check(reimported.LastActiveEpoch, Equals, uint64(42))
	c.Check(len(reimported.History), Equals, 2)
	c.Check(reimported.History[1].BpsAtClose, Equals, uint64(7))

	reimportedAcc, err := k2.GetDynamicFeeAccumulator(ctx2, "bentley", "BTC.BTC", "THOR.RUNE")
	c.Assert(err, IsNil)
	c.Check(reimportedAcc.VolumeTor.Equal(cosmos.NewUint(333)), Equals, true)
	c.Check(reimportedAcc.FeesTor.Equal(cosmos.NewUint(33)), Equals, true)

	observed, err := k2.GetLastObservedDynamicFeeEpochBlocks(ctx2)
	c.Assert(err, IsNil)
	c.Check(observed, Equals, int64(1337))

	reExported := ExportGenesis(ctx2, k2)
	c.Check(len(reExported.DynamicFeeRecords), Equals, 1)
	c.Check(len(reExported.DynamicFeeAccumulators), Equals, 1)
	c.Check(reExported.LastObservedDynamicFeeEpochBlocks, Equals, int64(1337))
}

func (GenesisTestSuite) TestGenesisReferenceMemoWindowRoundTrip(c *C) {
	SetupConfigForTest()

	srcCtx, srcMgr := setupManagerForTest(c)
	srcCtx = srcCtx.WithBlockHeight(1000)
	ttl := srcMgr.Keeper().GetConfigInt64(srcCtx, constants.MemolessTxnTTL)

	for i := 1; i <= 3; i++ {
		srcMgr.Keeper().SetReferenceMemo(srcCtx, types.ReferenceMemo{
			Asset:     common.BTCAsset,
			Reference: fmt.Sprintf("0000%d", i),
			Memo:      "+:BTC.BTC",
			Height:    srcCtx.BlockHeight(),
		})
	}
	srcMgr.Keeper().SetReferenceMemo(srcCtx, types.ReferenceMemo{
		Asset:     common.ETHAsset,
		Reference: "00001",
		Memo:      "+:ETH.ETH",
		Height:    srcCtx.BlockHeight(),
	})

	exported := ExportGenesis(srcCtx, srcMgr.Keeper())

	dstCtx, dstMgr := setupManagerForTest(c)
	dstCtx = dstCtx.WithBlockHeight(srcCtx.BlockHeight())
	InitGenesis(dstCtx, dstMgr.Keeper(), exported)

	// the window is not a genesis field, it is rebuilt from the imported memos so
	// the congestion price does not reset to "empty ring" across a fork
	c.Check(memolessLiveRefs(dstCtx, dstMgr.Keeper(), common.BTCAsset, ttl), Equals, int64(3))
	c.Check(memolessLiveRefs(dstCtx, dstMgr.Keeper(), common.ETHAsset, ttl), Equals, int64(1))

	// and it decays away as the imported memos expire
	c.Check(memolessLiveRefs(dstCtx.WithBlockHeight(dstCtx.BlockHeight()+ttl), dstMgr.Keeper(), common.BTCAsset, ttl), Equals, int64(0))
}

// TCY smart contract addresses are surfaced by ListTCYStakers as synthetic stakers whose
// amount is the contract's own TCY balance. That balance is exported by the bank module,
// so exporting it again as a staker record would double count it and, if the smart
// contract address list ever changed, import as real TCYStake module backed stake.
func (GenesisTestSuite) TestGenesisExportSkipsTCYSmartContractStakers(c *C) {
	SetupConfigForTest()
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	scAddr, _ := tcySmartContractStaker(c, ctx, k, cosmos.NewUint(1234))

	staker := GetRandomRUNEAddress()
	c.Assert(k.SetTCYStaker(ctx, types.NewTCYStaker(staker, cosmos.NewUint(500))), IsNil)

	// the synthetic staker is visible through the keeper
	stakers, err := k.ListTCYStakers(ctx)
	c.Assert(err, IsNil)
	foundSC := false
	for _, s := range stakers {
		if s.Address.Equals(scAddr) {
			foundSC = true
		}
	}
	c.Assert(foundSC, Equals, true)

	// ...but not through the genesis export
	exported := ExportGenesis(ctx, k)
	foundStaker := false
	for _, s := range exported.TcyStakers {
		c.Assert(tcysmartcontract.IsTCYSmartContractAddress(s.Address), Equals, false)
		if s.Address.Equals(staker) {
			foundStaker = true
			c.Assert(s.Amount.Equal(cosmos.NewUint(500)), Equals, true)
		}
	}
	c.Assert(foundStaker, Equals, true)
}

// TestGenesisSolVaultNonceRoundTrip: a vault's committed durable nonce survives
// export/import (case-sensitive base58 values intact), so the vault is not treated as
// legacy afterwards; invalid records fail genesis validation.
func (GenesisTestSuite) TestGenesisSolVaultNonceRoundTrip(c *C) {
	SetupConfigForTest()

	srcCtx, srcMgr := setupManagerForTest(c)
	vaultPK := GetRandomPubKey()
	record := SolVaultNonce{
		VaultPubKey:   vaultPK,
		NonceAccount:  "9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo",
		Nonce:         "C8976AEb5Q4QFVZQQZ7rNY3u2j4n83PPHNagHeJLeCkH",
		Slot:          1000,
		UpdatedHeight: 42,
		SetByTx:       GetRandomTxHash(),
	}
	srcMgr.Keeper().SetSolVaultNonce(srcCtx, record)

	exported := ExportGenesis(srcCtx, srcMgr.Keeper())
	c.Assert(exported.SolVaultNonces, HasLen, 1)
	c.Assert(ValidateGenesis(exported), IsNil)
	bz, err := exported.Marshal()
	c.Assert(err, IsNil)
	var decoded GenesisState
	c.Assert(decoded.Unmarshal(bz), IsNil)

	dstCtx, dstMgr := setupManagerForTest(c)
	InitGenesis(dstCtx, dstMgr.Keeper(), decoded)
	got, found, err := dstMgr.Keeper().GetSolVaultNonce(dstCtx, vaultPK)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(got.NonceAccount, Equals, record.NonceAccount)
	c.Check(got.Nonce, Equals, record.Nonce)
	c.Check(got.Slot, Equals, record.Slot)
	c.Check(got.UpdatedHeight, Equals, record.UpdatedHeight)
	c.Check(got.SetByTx.Equals(record.SetByTx), Equals, true)

	bad := decoded
	bad.SolVaultNonces = []SolVaultNonce{{VaultPubKey: vaultPK, NonceAccount: record.NonceAccount, Slot: 1}}
	c.Assert(ValidateGenesis(bad), NotNil)
}

func (GenesisTestSuite) TestExportSwapperCloutBase58(c *C) {
	ctx, mgr := setupManagerForTest(c)
	transparent, _, _, _ := zecTxOutAddresses()
	clout := NewSwapperClout(transparent)
	clout.Score = cosmos.NewUint(1000)
	c.Assert(mgr.Keeper().SetSwapperClout(ctx, clout), IsNil)

	// Store keys are uppercased, so the export must not rebuild base58 addresses from them.
	gs := ExportGenesis(ctx, mgr.Keeper())
	c.Assert(gs.SwapperClout, HasLen, 1)
	c.Check(gs.SwapperClout[0].Address, Equals, transparent)
	c.Check(gs.SwapperClout[0].Score.String(), Equals, "1000")
}
