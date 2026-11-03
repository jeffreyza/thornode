// Package dashutil adapts the upstream Dash library to Bifrost's supported
// address types and external signing flow.
package dashutil

import (
	"fmt"

	"github.com/dashpay/dashd-go/btcutil"
	"github.com/dashpay/dashd-go/chaincfg"
)

// DecodeAddress accepts only Dash payment addresses on the requested network.
// dashd-go v0.26.1 retains Bitcoin's witness address support and accepts bare
// public keys; neither is a supported Dash destination in Bifrost.
func DecodeAddress(address string, params *chaincfg.Params) (btcutil.Address, error) {
	decoded, err := btcutil.DecodeAddress(address, params)
	if err != nil {
		return nil, err
	}
	if !decoded.IsForNet(params) {
		return nil, fmt.Errorf("Dash address belongs to another network")
	}
	switch decoded.(type) {
	case *btcutil.AddressPubKeyHash, *btcutil.AddressScriptHash:
		return decoded, nil
	default:
		return nil, fmt.Errorf("unsupported Dash address type %T", decoded)
	}
}
