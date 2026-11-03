package common

import (
	"github.com/btcsuite/btcd/chaincfg"
	dogchaincfg "github.com/eager7/dogd/chaincfg"
	ltcchaincfg "github.com/ltcsuite/ltcd/chaincfg"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type ChainSuite struct{}

var _ = Suite(&ChainSuite{})

func (s ChainSuite) TestChain(c *C) {
	ethChain, err := NewChain("eth")
	c.Assert(err, IsNil)
	c.Check(ethChain.Equals(ETHChain), Equals, true)
	c.Check(ethChain.IsEmpty(), Equals, false)
	c.Check(ethChain.String(), Equals, "ETH")

	_, err = NewChain("B") // too short
	c.Assert(err, NotNil)

	chains := Chains{"DOGE", "DOGE", "BTC"}
	c.Check(chains.Has("BTC"), Equals, true)
	c.Check(chains.Has("ETH"), Equals, false)
	uniq := chains.Distinct()
	c.Assert(uniq, HasLen, 2)

	algo := ETHChain.GetSigningAlgo()
	c.Assert(algo, Equals, SigningAlgoSecp256k1)
	c.Assert(SOLChain.GetSigningAlgo(), Equals, SigningAlgoEd25519)
	c.Assert(XMRChain.GetSigningAlgo(), Equals, SigningAlgoEd25519)

	c.Assert(BTCChain.GetGasAsset(), Equals, BTCAsset)
	c.Assert(ETHChain.GetGasAsset(), Equals, ETHAsset)
	c.Assert(LTCChain.GetGasAsset(), Equals, LTCAsset)
	c.Assert(BCHChain.GetGasAsset(), Equals, BCHAsset)
	c.Assert(DOGEChain.GetGasAsset(), Equals, DOGEAsset)
	c.Assert(GAIAChain.GetGasAsset(), Equals, ATOMAsset)
	c.Assert(NOBLEChain.GetGasAsset(), Equals, USDCAsset)
	c.Assert(TRONChain.GetGasAsset(), Equals, TRXAsset)
	c.Assert(XMRChain.GetGasAsset(), Equals, XMRAsset)
	c.Assert(EmptyChain.GetGasAsset(), Equals, EmptyAsset)

	c.Assert(BTCChain.AddressPrefix(MockNet), Equals, chaincfg.RegressionNetParams.Bech32HRPSegwit)
	c.Assert(BTCChain.AddressPrefix(MainNet), Equals, chaincfg.MainNetParams.Bech32HRPSegwit)
	c.Assert(BTCAsset.Chain.AddressPrefix(StageNet), Equals, chaincfg.MainNetParams.Bech32HRPSegwit)

	c.Assert(LTCChain.AddressPrefix(MockNet), Equals, ltcchaincfg.RegressionNetParams.Bech32HRPSegwit)
	c.Assert(LTCChain.AddressPrefix(MainNet), Equals, ltcchaincfg.MainNetParams.Bech32HRPSegwit)
	c.Assert(LTCChain.AddressPrefix(StageNet), Equals, ltcchaincfg.MainNetParams.Bech32HRPSegwit)

	c.Assert(DOGEChain.AddressPrefix(MockNet), Equals, dogchaincfg.RegressionNetParams.Bech32HRPSegwit)
	c.Assert(DOGEChain.AddressPrefix(MainNet), Equals, dogchaincfg.MainNetParams.Bech32HRPSegwit)
	c.Assert(DOGEChain.AddressPrefix(StageNet), Equals, dogchaincfg.MainNetParams.Bech32HRPSegwit)
	c.Assert(XMRChain.AddressPrefix(MockNet), Equals, "4")
	c.Assert(XMRChain.AddressPrefix(MainNet), Equals, "4")
	c.Assert(XMRChain.AddressPrefix(StageNet), Equals, "4")

	// Noble chain tests
	c.Assert(NOBLEChain.AddressPrefix(MockNet), Equals, "noble")
	c.Assert(NOBLEChain.AddressPrefix(MainNet), Equals, "noble")
	c.Assert(NOBLEChain.AddressPrefix(StageNet), Equals, "noble")

	// Test GetGasUnits
	nobleGasUnits, _ := NOBLEChain.GetGasUnits()
	gaiaGasUnits, _ := GAIAChain.GetGasUnits()
	c.Assert(nobleGasUnits, Equals, "uusdc")
	c.Assert(gaiaGasUnits, Equals, "uatom")

	// Test GetGasAssetDecimal
	c.Assert(NOBLEChain.GetGasAssetDecimal(), Equals, int64(6))
	c.Assert(GAIAChain.GetGasAssetDecimal(), Equals, int64(6))

	// Test ApproximateBlockMilliseconds
	c.Assert(NOBLEChain.ApproximateBlockMilliseconds(), Equals, int64(1500))
	c.Assert(GAIAChain.ApproximateBlockMilliseconds(), Equals, int64(6000))

	// Test InboundNotes
	c.Assert(NOBLEChain.InboundNotes(), Equals, "Transfer the inbound_address the asset with the memo. Do not use multi-in, multi-out transactions.")
	c.Assert(GAIAChain.InboundNotes(), Equals, "Transfer the inbound_address the asset with the memo. Do not use multi-in, multi-out transactions.")

	// Tron chain tests
	tronGasUnits, _ := TRONChain.GetGasUnits()
	c.Assert(tronGasUnits, Equals, "sun")
	c.Assert(TRONChain.GetGasAssetDecimal(), Equals, int64(6))
	c.Assert(TRONChain.ApproximateBlockMilliseconds(), Equals, int64(3000))

	// TAO chain tests
	c.Assert(TAOChain.GetGasAsset(), Equals, TAOAsset)
	c.Assert(TAOChain.GetSigningAlgo(), Equals, SigningAlgoEd25519)
	c.Assert(TAOChain.AddressPrefix(MockNet), Equals, "5")
	c.Assert(TAOChain.AddressPrefix(MainNet), Equals, "5")
	taoGasUnits, taoGasPerOne := TAOChain.GetGasUnits()
	c.Assert(taoGasUnits, Equals, "rao")
	c.Assert(taoGasPerOne.Uint64(), Equals, uint64(1e9))
	c.Assert(TAOChain.GetGasAssetDecimal(), Equals, int64(9))
	c.Assert(TAOChain.ApproximateBlockMilliseconds(), Equals, int64(12000))
	c.Assert(TAOChain.DustThreshold().Uint64(), Equals, uint64(100_000))
	c.Assert(TAOChain.InboundNotes(), Not(Equals), "")

	// DOT chain tests
	c.Assert(DOTChain.GetGasAsset(), Equals, DOTAsset)
	c.Assert(DOTChain.GetSigningAlgo(), Equals, SigningAlgoEd25519)
	c.Assert(DOTChain.AddressPrefix(MockNet), Equals, "5")
	c.Assert(DOTChain.AddressPrefix(MainNet), Equals, "1")
	c.Assert(DOTChain.AddressPrefix(StageNet), Equals, "1")

	// XMR chain tests
	xmrGasUnits, _ := XMRChain.GetGasUnits()
	c.Assert(xmrGasUnits, Equals, "piconero")
	c.Assert(XMRChain.GetGasAssetDecimal(), Equals, int64(12))
	c.Assert(XMRChain.ApproximateBlockMilliseconds(), Equals, int64(120000))
	c.Assert(XMRChain.RequiresExplicitRefundAddress(), Equals, true)
	c.Assert(BTCChain.RequiresExplicitRefundAddress(), Equals, false)
	c.Assert(TRONChain.SupportsSolvencyObservationSlashing(), Equals, false)
	c.Assert(Chain("tron").SupportsSolvencyObservationSlashing(), Equals, false)
	c.Assert(ETHChain.SupportsSolvencyObservationSlashing(), Equals, true)
}

// NativeGasToThorCoinCeil rounds up when the gas asset has more decimals
// than THORChain (1e8) and any sub-1e8 native precision would otherwise
// be silently dropped. Exercise both the exact-amount path (no ceil)
// and the lossy path (+1 thor unit) on DOT (1e10 planck) and TAO (1e9 rao).
func (s ChainSuite) TestNativeGasToThorCoinCeil(c *C) {
	// DOT: 1e10 planck per DOT, 1e8 thor units per DOT, so 100 planck = 1 thor unit.
	c.Assert(DOTChain.NativeGasToThorCoinCeil(cosmos.NewUint(100)).Amount.Uint64(),
		Equals, uint64(1)) // exact: no ceil
	c.Assert(DOTChain.NativeGasToThorCoinCeil(cosmos.NewUint(105)).Amount.Uint64(),
		Equals, uint64(2)) // 5 planck dropped → +1
	c.Assert(DOTChain.NativeGasToThorCoinCeil(cosmos.NewUint(199)).Amount.Uint64(),
		Equals, uint64(2)) // 99 planck dropped → +1
	c.Assert(DOTChain.NativeGasToThorCoinCeil(cosmos.NewUint(0)).Amount.Uint64(),
		Equals, uint64(0)) // zero stays zero
	c.Assert(DOTChain.NativeGasToThorCoinCeil(cosmos.NewUint(100)).Decimals,
		Equals, int64(10))

	// TAO: 1e9 rao per TAO, 1e8 thor units per TAO, so 10 rao = 1 thor unit.
	c.Assert(TAOChain.NativeGasToThorCoinCeil(cosmos.NewUint(10)).Amount.Uint64(),
		Equals, uint64(1))
	c.Assert(TAOChain.NativeGasToThorCoinCeil(cosmos.NewUint(15)).Amount.Uint64(),
		Equals, uint64(2))
	c.Assert(TAOChain.NativeGasToThorCoinCeil(cosmos.NewUint(19)).Amount.Uint64(),
		Equals, uint64(2))
	c.Assert(TAOChain.NativeGasToThorCoinCeil(cosmos.NewUint(0)).Amount.Uint64(),
		Equals, uint64(0))
	c.Assert(TAOChain.NativeGasToThorCoinCeil(cosmos.NewUint(10)).Decimals,
		Equals, int64(9))

	// Chains with decimal <= 8 take the no-ceil path.
	c.Assert(BTCChain.NativeGasToThorCoinCeil(cosmos.NewUint(50_000_000)).Amount.Uint64(),
		Equals, BTCChain.NativeGasToThorCoin(cosmos.NewUint(50_000_000)).Amount.Uint64())

	// Dash chain tests
	c.Assert(DASHChain.IsUTXO(), Equals, true)
	c.Assert(DASHChain.GetGasAsset(), Equals, DASHAsset)

	gasUnit, perOne := DASHChain.GetGasUnits()
	c.Assert(gasUnit, Equals, "satsperbyte")
	c.Assert(perOne.Uint64(), Equals, uint64(1e8))

	// P2WPKHOutputValue is the dust-funding value for extended-memo overflow
	// outputs. For Dash (no SegWit, P2PKH-only) this must match Bitcoin Core's
	// legacy P2PKH dust default (546 sats), not the P2WPKH-aware 294 used by BTC.
	c.Assert(DASHChain.P2WPKHOutputValue(), Equals, int64(546))

	c.Assert(DASHChain.MaxMemoLength() > 0, Equals, true)
	c.Assert(DASHChain.ApproximateBlockMilliseconds(), Equals, int64(150_000))
}
