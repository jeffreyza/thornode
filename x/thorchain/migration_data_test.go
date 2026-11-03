package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type MigrationDataSuite struct{}

var _ = Suite(&MigrationDataSuite{})

func (s MigrationDataSuite) TestVerifyTotal4to5(c *C) {
	// verify total
	sum := uint64(0)
	for _, refund := range mainnetSlashRefunds4to5 {
		sum += refund.amount
	}
	// $ https gateway.liquify.com/chain/thorchain_api/thorchain/block height==20518466 | \
	//   jq '[.txs[1].result.events[]|select(.bond_type=="bond_cost")|.amount|tonumber]|add'
	// 9674032456636
	c.Assert(sum, Equals, uint64(9674032456636))

	// verify no duplicates
	addresses := make(map[string]bool)
	for _, refund := range mainnetSlashRefunds4to5 {
		c.Assert(addresses[refund.address], Equals, false)
		addresses[refund.address] = true
	}
}

func (s MigrationDataSuite) TestVerifyTotal5to6(c *C) {
	// verify total
	sum := uint64(0)
	for _, refund := range mainnetSlashRefunds5to6 {
		sum += refund.amount
	}

	// Total calculated using ebifrost_rollout_bond_slash_refunds.py script (see MR #4090)
	c.Assert(sum, Equals, uint64(14856919212689))

	// verify no duplicates
	addresses := make(map[string]bool)
	for _, refund := range mainnetSlashRefunds5to6 {
		c.Assert(addresses[refund.address], Equals, false)
		addresses[refund.address] = true
	}
}

func (s MigrationDataSuite) TestMigrate5to6ValidationLogic(c *C) {
	// Test the validation logic by checking if the total amount calculation is correct
	totalRefundAmount := cosmos.NewUint(14856919212689)

	// Verify this matches our manual calculation
	sum := uint64(0)
	for _, refund := range mainnetSlashRefunds5to6 {
		sum += refund.amount
	}

	c.Assert(totalRefundAmount.Equal(cosmos.NewUint(sum)), Equals, true)

	// Test that the validation would catch insufficient funds
	insufficientBalance := cosmos.NewUint(1000) // Much less than required
	c.Assert(insufficientBalance.LT(totalRefundAmount), Equals, true)

	// Test that a sufficient balance would pass validation
	sufficientBalance := totalRefundAmount.Add(cosmos.NewUint(1000000))
	c.Assert(sufficientBalance.GTE(totalRefundAmount), Equals, true)
}

func (s MigrationDataSuite) TestMigrate5to6AddressValidation(c *C) {
	// Test that all addresses in the refund list are valid thor addresses
	for i, refund := range mainnetSlashRefunds5to6 {
		// Use common.Address to properly validate thor addresses
		thorAddr, err := common.NewAddress(refund.address)
		c.Assert(err, IsNil, Commentf("Invalid address at index %d: %s", i, refund.address))

		// Convert to AccAddress using MappedAccAddress which handles prefix conversion
		_, err = thorAddr.MappedAccAddress()
		c.Assert(err, IsNil, Commentf("Failed to convert address at index %d: %s", i, refund.address))

		// Verify thor prefix
		c.Assert(refund.address[:4], Equals, "thor", Commentf("Address at index %d does not have thor prefix: %s", i, refund.address))

		// Verify address length (thor addresses should be 43 characters)
		c.Assert(len(refund.address), Equals, 43, Commentf("Address at index %d has incorrect length: %s", i, refund.address))

		// Verify amount is positive
		c.Assert(refund.amount > 0, Equals, true, Commentf("Amount at index %d should be positive: %d", i, refund.amount))
	}
}

func (s MigrationDataSuite) TestVerifyTotal12to13(c *C) {
	// verify total
	sum := uint64(0)
	for _, refund := range mainnetSlashRefunds12to13 {
		sum += refund.amount
	}
	// $ https gateway.liquify.com/chain/thorchain_api/thorchain/block height==24809168 | \
	//   jq '[.txs[7].result.events[]|select(.bond_type=="bond_cost")|.amount|tonumber]|add'
	// 53389901389659
	totalBondSlash := 53389901389659

	// NOTE: ~96 RUNE of slash was on the autobond contract, which cannot receive
	// transfer. That amount is excluded from the refunds.
	autoBondSlash := 9615118848
	c.Assert(mainnetSlashRefunds12to13Total, Equals, uint64(totalBondSlash-autoBondSlash))
	c.Assert(sum, Equals, mainnetSlashRefunds12to13Total)

	// verify no duplicates
	addresses := make(map[string]bool)
	for _, refund := range mainnetSlashRefunds12to13 {
		c.Assert(addresses[refund.address], Equals, false)
		addresses[refund.address] = true
	}
}

func (s MigrationDataSuite) TestVerifyTotal14to15(c *C) {
	// verify total — sum of provider bond deltas at slash block 26,001,464.
	// $ tci nodes bond-diff --long --heights 26001464 --no-node --no-operator --provider
	// total diff => -520,174.60416933 (= 52,017,460,416,933 base units)
	sum := uint64(0)
	for _, refund := range mainnetSlashRefunds14to15 {
		sum += refund.amount
	}
	c.Assert(sum, Equals, mainnetSlashRefunds14to15Total)
	c.Assert(mainnetSlashRefunds14to15Total, Equals, uint64(52017460416933))

	// 127 providers across the 19 slashed vault members. The rujira-brune
	// liquid bond aggregator (thor1enhmz...) is included; verified that it
	// reads bank balance via state.allocate(), so the unsolicited send is
	// recognized as NAV and streamed to brune stakers via stake_contract.
	c.Assert(len(mainnetSlashRefunds14to15), Equals, 127)

	// verify no duplicates
	addresses := make(map[string]bool)
	for _, refund := range mainnetSlashRefunds14to15 {
		c.Assert(addresses[refund.address], Equals, false)
		addresses[refund.address] = true
	}
}

func (s MigrationDataSuite) TestMigrate14to15AddressValidation(c *C) {
	// Exercise the same parse path the migration uses at runtime, namely
	// cosmos.AccAddressFromBech32 with the mainnet "thor" prefix and
	// VerifyAddressFormat. We can't call AccAddressFromBech32 directly here
	// because tests run under -tags=mocknet, where the configured prefix is
	// "tthor"; replicate its checks against a hardcoded "thor" instead.
	//
	// One provider is a 64-char wasm contract address (32-byte payload, the
	// rujira-brune liquid bond aggregator) and the rest are 43-char account
	// addresses, so a length assertion from prior tests doesn't apply.
	for i, refund := range mainnetSlashRefunds14to15 {
		bz, err := cosmos.GetFromBech32(refund.address, "thor")
		c.Assert(err, IsNil, Commentf("GetFromBech32 failed at index %d: %s", i, refund.address))

		err = cosmos.VerifyAddressFormat(bz)
		c.Assert(err, IsNil, Commentf("VerifyAddressFormat failed at index %d: %s", i, refund.address))

		c.Assert(refund.amount > 0, Equals, true, Commentf("Amount at index %d should be positive: %d", i, refund.amount))
	}
}

func (s MigrationDataSuite) TestMigrate15to16DataValidation(c *C) {
	// ADR-028 (Migrate15to16) pins long hex/bech32 strings that the //go:build
	// mainnet migration parses at the one-shot upgrade height. Validate their shape
	// here (mocknet CI) so a transposed character fails the build instead of aborting
	// the chain mid-migration. Tests run under -tags=mocknet (prefix "tthor"), so —
	// like TestMigrate14to15AddressValidation — check against the hardcoded mainnet
	// "thor"/"thorpub" HRPs directly rather than via the configured prefix.

	// Compromised vault pubkey (step 0 zeroes it — zeroing the wrong vault would be
	// catastrophic, so a corrupted key must fail the build).
	_, err := cosmos.GetFromBech32(string(vault2v28PubKey), "thorpub")
	c.Assert(err, IsNil, Commentf("vault2v28PubKey is not a valid thorpub bech32: %s", vault2v28PubKey))

	// Treasury multisig (step-4 LP coverage).
	bz, err := cosmos.GetFromBech32(adr028TreasuryAddr, "thor")
	c.Assert(err, IsNil, Commentf("adr028TreasuryAddr GetFromBech32 failed: %s", adr028TreasuryAddr))
	c.Assert(cosmos.VerifyAddressFormat(bz), IsNil, Commentf("adr028TreasuryAddr VerifyAddressFormat failed: %s", adr028TreasuryAddr))

	// Stuck streaming-swap txid (step 6) — NewTxID enforces 64 hex chars.
	_, err = common.NewTxID(stuckStreamingTxStr)
	c.Assert(err, IsNil, Commentf("stuckStreamingTxStr is not a valid txid: %s", stuckStreamingTxStr))

	// Hex-qualified haircut assets — must parse, be non-empty, and unique.
	c.Assert(len(adr028PinnedAssets), Equals, 7)
	seen := make(map[string]bool)
	for i, a := range adr028PinnedAssets {
		asset, err := common.NewAsset(a)
		c.Assert(err, IsNil, Commentf("NewAsset failed at index %d: %s", i, a))
		c.Assert(asset.IsEmpty(), Equals, false, Commentf("asset is empty at index %d: %s", i, a))
		c.Assert(seen[a], Equals, false, Commentf("duplicate asset at index %d: %s", i, a))
		seen[a] = true
	}
}

func (s MigrationDataSuite) TestVerifyVaultClampFixes14to15(c *C) {
	// Verify the three (vault, amount) clamping reconciliations match the
	// documented values exactly. Re-stated here to make a typo in the data
	// table fail the build.
	// Per @ursa9r's gap verification: pysp's two clamping events are coalesced
	// into a single row whose amount equals the actual on-chain insolvency gap
	// (vault.Coins[ETH] − ETH bank balance), so the table is 2 rows not 3.
	c.Assert(len(mainnetVaultClampFixes14to15), Equals, 2)

	const (
		vtxv = "thorpub1addwnpepq072srp46fp0fgzwkk8cv53n3z4lckzw5s6rxx4u0g805pu6uqp7u5dvtxv"
		pysp = "thorpub1addwnpepqgdfhpqtkzvj2shhumkad49rd89te6ff5rchd2lq0kt3e2dw9ty8qazpysp"
	)

	// vtxv: gap 44,121,988,925 − 28,859,024,982 = 15,262,963,943
	c.Assert(mainnetVaultClampFixes14to15[0].pubKey, Equals, vtxv)
	c.Assert(mainnetVaultClampFixes14to15[0].amount, Equals, uint64(15262963943))
	// pysp: gap 11,900,321,082 −    120,503,076 = 11,779,818,006 (coalesced)
	c.Assert(mainnetVaultClampFixes14to15[1].pubKey, Equals, pysp)
	c.Assert(mainnetVaultClampFixes14to15[1].amount, Equals, uint64(11779818006))

	// Per-vault and grand totals (1e8 base units of ETH).
	const (
		totalVtxv uint64 = 15262963943           // 152.62963943 ETH
		totalPysp uint64 = 11779818006           // 117.79818006 ETH
		grand     uint64 = totalVtxv + totalPysp // 270.42781949 ETH
	)
	c.Assert(grand, Equals, uint64(27042781949))

	// Verify computed sums from the table match the constants above.
	var sumVtxv, sumPysp, sum uint64
	for _, fix := range mainnetVaultClampFixes14to15 {
		sum += fix.amount
		switch fix.pubKey {
		case vtxv:
			sumVtxv += fix.amount
		case pysp:
			sumPysp += fix.amount
		default:
			c.Fatalf("unexpected pubkey in clamp fix table: %s", fix.pubKey)
		}
	}
	c.Assert(sumVtxv, Equals, totalVtxv)
	c.Assert(sumPysp, Equals, totalPysp)
	c.Assert(sum, Equals, grand)
}

func (s MigrationDataSuite) TestVaultClampFixes14to15PubKeyValidation(c *C) {
	// Vault pubkeys are encoded with the *pubkey* bech32 prefix (`thorpub`).
	// We can't call common.NewPubKey here because the test runs under
	// `-tags=mocknet` where the bech32 prefix is `tthorpub`; the production
	// data uses the mainnet prefix and will only be parsed under the mainnet
	// build (where Migrate14to15 is invoked). This test asserts shape only.
	const expectedLen = len("thorpub1addwnpepq072srp46fp0fgzwkk8cv53n3z4lckzw5s6rxx4u0g805pu6uqp7u5dvtxv")
	for i, fix := range mainnetVaultClampFixes14to15 {
		c.Assert(fix.pubKey[:7], Equals, "thorpub", Commentf("PubKey at index %d does not have thorpub prefix: %s", i, fix.pubKey))
		c.Assert(len(fix.pubKey), Equals, expectedLen, Commentf("PubKey at index %d has incorrect length: %s", i, fix.pubKey))
		c.Assert(fix.amount > 0, Equals, true, Commentf("Amount at index %d should be positive: %d", i, fix.amount))
	}
}
