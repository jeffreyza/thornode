// Package pivxutil encodes PIVX addresses and legacy transparent signatures.
//
// PIVX version-1 transactions store nVersion and nType as two little-endian
// int16 fields. With nType 0 that is the same four bytes as a Bitcoin version-1
// transaction, and SIGVERSION_BASE uses the legacy SIGHASH_ALL digest. This
// package therefore signs with Bitcoin's legacy sighash and rejects every
// other transaction shape.
package pivxutil

import "github.com/btcsuite/btcd/chaincfg"

// Address version bytes from PIVX src/chainparams.cpp (master, v5.6).
// Regtest copies the testnet prefixes.
const (
	MainNetPubKeyHashID byte = 30  // 'D'
	MainNetScriptHashID byte = 13  // '6' or '7'
	MainNetSecretKeyID  byte = 212 // WIF
	MainNetStakingID    byte = 63  // 'S'

	RegTestPubKeyHashID byte = 139 // 'x' or 'y'
	RegTestScriptHashID byte = 19  // '8' or '9'
	RegTestSecretKeyID  byte = 239
	RegTestStakingID    byte = 73 // 'W'

	// Exchange addresses are a three-byte version. The script pays to
	// OP_EXCHANGEADDR and is not a supported outbound destination.
	// Mainnet base58 starts with EXM; testnet/regtest starts with EXT.
	mainNetExchange0 = 0x01
	mainNetExchange1 = 0xb9
	mainNetExchange2 = 0xa2
	regTestExchange2 = 0xb1
)

// MaxDataCarrierBytes is the OP_RETURN payload limit. PIVX MAX_OP_RETURN_RELAY
// is 83 bytes for the whole script (OP_RETURN + push opcode + data), which
// allows an 80-byte memo.
const MaxDataCarrierBytes = 80

// FallbackFeeRateSatsPerByte is minRelayTxFee (10_000 sat/KB) expressed per
// byte. estimatesmartfee is deprecated and returns -1 until it has samples.
const FallbackFeeRateSatsPerByte uint64 = 10

var (
	mainNetParams = chaincfg.Params{
		Name:             "pivx",
		PubKeyHashAddrID: MainNetPubKeyHashID,
		ScriptHashAddrID: MainNetScriptHashID,
		PrivateKeyID:     MainNetSecretKeyID,
	}
	regressionNetParams = chaincfg.Params{
		Name:             "pivx-regtest",
		PubKeyHashAddrID: RegTestPubKeyHashID,
		ScriptHashAddrID: RegTestScriptHashID,
		PrivateKeyID:     RegTestSecretKeyID,
	}
)

// MainNetParams are the mainnet base58 versions. Regtest and testnet share
// RegressionNetParams.
func MainNetParams() *chaincfg.Params { return &mainNetParams }

// NetworkParams selects regtest parameters when mocknet is true.
func NetworkParams(mocknet bool) *chaincfg.Params {
	if mocknet {
		return RegressionNetParams()
	}
	return MainNetParams()
}

// RegressionNetParams are the regtest/testnet base58 versions.
func RegressionNetParams() *chaincfg.Params { return &regressionNetParams }
