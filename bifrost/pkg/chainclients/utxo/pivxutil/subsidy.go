package pivxutil

import (
	"strconv"

	"github.com/btcsuite/btcutil"
)

// BlockSubsidySats is PIVX GetBlockValue (src/validation.cpp) in satoshis.
//
// regtest follows the regtest activation heights in chainparams.cpp: 250 PIV
// until UPGRADE_V5_5 (height 576), then 10 PIV. The mainnet schedule is used
// for every other network. Testnet's height-2..200 inflation burst is not
// represented; mocknet talks to regtest.
//
// The value is the whole block subsidy. Masternode and budget payments are
// slices of it (or, on a superblock, an extra budget payment). Callers must
// not add coinstake outputs on top: those outputs also return the staked
// principal.
func BlockSubsidySats(regtest bool, height int64) int64 {
	const coin int64 = 100_000_000
	if height <= 0 {
		return 0
	}
	if regtest {
		// UPGRADE_V5_5 activates at height 576 on regtest. GetBlockValue pays
		// the reduced reward strictly after that height.
		if height > 576 {
			return 10 * coin
		}
		return 250 * coin
	}
	if height == 1 {
		return 60001 * coin
	}
	// Mainnet upgrade heights from chainparams.cpp.
	if height > 3_715_200 { // UPGRADE_V5_5
		return 10 * coin
	}
	if height > 1_153_160 { // UPGRADE_ZC_V2
		return 5 * coin
	}
	if height > 648000 {
		return 45 * coin / 10
	}
	if height > 604800 {
		return 9 * coin
	}
	if height > 561600 {
		return 135 * coin / 10
	}
	if height > 518400 {
		return 18 * coin
	}
	if height > 475200 {
		return 225 * coin / 10
	}
	if height > 432000 {
		return 27 * coin
	}
	if height > 388800 {
		return 315 * coin / 10
	}
	if height > 345600 {
		return 36 * coin
	}
	if height > 302400 {
		return 405 * coin / 10
	}
	if height > 151200 {
		return 45 * coin
	}
	if height > 86400 {
		return 225 * coin
	}
	return 250 * coin
}

// FeeRateSatsPerByte converts getblockindexstats feeperkb (PIV per KB, as
// FormatMoney text) into satoshis per byte. A non-positive rate or an empty
// byte window is not a sample; callers then use FallbackFeeRateSatsPerByte.
func FeeRateSatsPerByte(feePerKB string, txBytes int64) (uint64, bool) {
	if txBytes <= 0 || feePerKB == "" {
		return 0, false
	}
	pivPerKB, err := strconv.ParseFloat(feePerKB, 64)
	if err != nil || pivPerKB <= 0 {
		return 0, false
	}
	amt, err := btcutil.NewAmount(pivPerKB)
	if err != nil || amt <= 0 {
		return 0, false
	}
	satsPerKB := uint64(amt)
	rate := satsPerKB / 1000
	if satsPerKB%1000 != 0 {
		rate++
	}
	if rate == 0 {
		return 0, false
	}
	return rate, true
}
