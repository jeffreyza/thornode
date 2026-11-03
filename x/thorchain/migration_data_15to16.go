package thorchain

import "gitlab.com/thorchain/thornode/v3/common"

// Pinned data for Migrate15to16 (ADR-028 conciliation). It lives in this
// build-tag-agnostic file — the migration LOGIC is //go:build mainnet — so a
// mocknet-buildable data-validation test (TestMigrate15to16DataValidation in
// migration_data_test.go) can assert these parse and have the right shape,
// catching a transposed hex character or a corrupted key BEFORE the one-shot
// upgrade height (the same precedent as Migrate14to15's data tables here).

// vault2v28PubKey is the compromised Asgard vault whose keeper balance is zeroed
// in step 0. Its L1 assets churn to the new active vault via normal bifrost ops.
const vault2v28PubKey = common.PubKey("thorpub1addwnpepqvpnhwzqkd2zqkxsj272faf4ezsngwh773wjdfkus8m4f860u3g5ka22v28")

// adr028TreasuryAddr is the treasury multisig providing step-4 LP coverage.
const adr028TreasuryAddr = "thor10qh5272ktq4wes8ex343ky9rsuehcypddjh08k"

// stuckStreamingTxStr is the half-finished ETH.DAI→ETH.USDT streaming swap settled
// in step 6 (its DAI input was stranded in the drained 2v28 vault).
const stuckStreamingTxStr = "5071A7399C528E8D5BDC1BA62E87001EB5EB17E9E25ABF2965F6A38A665FF729"

// adr028 hex-qualified ERC20 assets the haircut table parses — the typo-prone
// strings (a transposed hex char yields a valid-format but wrong-token asset).
const (
	adr028AssetETHUSDT  = "ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7"
	adr028AssetAVAXUSDC = "AVAX.USDC-0XB97EF9EF8734C71904D8002F8B6BC66DD9C48A6E"
	adr028AssetBASEUSDC = "BASE.USDC-0X833589FCD6EDB6E08F4C7C32D4F71B54BDA02913"
	adr028AssetBSCBUSD  = "BSC.BUSD-0XE9E7CEA3DEDCA5984780BAFC599BD69ADD087D56"
	adr028AssetBSCETH   = "BSC.ETH-0X2170ED0880AC9A755FD29B2688956BD959F933F8"
	adr028AssetBSCBTCB  = "BSC.BTCB-0X7130D2A12B9BCBFAE4F2634D864A1EE1CE3EAD9C"
	adr028AssetETHUSDC  = "ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48"
)

// adr028PinnedAssets enumerates the hex-qualified assets above for the data test.
var adr028PinnedAssets = []string{
	adr028AssetETHUSDT, adr028AssetAVAXUSDC, adr028AssetBASEUSDC,
	adr028AssetBSCBUSD, adr028AssetBSCETH, adr028AssetBSCBTCB, adr028AssetETHUSDC,
}
