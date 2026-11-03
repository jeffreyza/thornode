package pivxutil

import (
	"fmt"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcutil"
	"github.com/btcsuite/btcutil/base58"
)

// DecodeAddress accepts a PIVX P2PKH or P2SH address on the requested network.
// Staking addresses (mainnet 'S', testnet/regtest 'W') and exchange addresses
// (mainnet EXM, testnet/regtest EXT) are rejected. Bare public keys and witness
// addresses are not PIVX payment destinations.
func DecodeAddress(address string, params *chaincfg.Params) (btcutil.Address, error) {
	if params == nil {
		return nil, fmt.Errorf("missing PIVX network params")
	}
	if err := rejectUnsupportedVersion(address); err != nil {
		return nil, err
	}
	decoded, err := btcutil.DecodeAddress(address, params)
	if err != nil {
		return nil, err
	}
	if !decoded.IsForNet(params) {
		return nil, fmt.Errorf("PIVX address belongs to another network")
	}
	switch decoded.(type) {
	case *btcutil.AddressPubKeyHash, *btcutil.AddressScriptHash:
		return decoded, nil
	default:
		return nil, fmt.Errorf("unsupported PIVX address type %T", decoded)
	}
}

// rejectUnsupportedVersion fails closed on staking and exchange versions
// before the single-byte P2PKH/P2SH decoder runs.
func rejectUnsupportedVersion(address string) error {
	payload, version, err := base58.CheckDecode(address)
	if err != nil {
		return nil
	}
	switch version {
	case MainNetStakingID, RegTestStakingID:
		if len(payload) == 20 {
			return fmt.Errorf("PIVX staking addresses are not valid destinations")
		}
	case mainNetExchange0:
		if len(payload) >= 22 && payload[0] == mainNetExchange1 &&
			(payload[1] == mainNetExchange2 || payload[1] == regTestExchange2) {
			return fmt.Errorf("PIVX exchange addresses are not valid destinations")
		}
	}
	return nil
}
