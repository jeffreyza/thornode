package dashutil

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/dashpay/dashd-go/btcec/v2"
	"github.com/dashpay/dashd-go/btcutil"
	"github.com/dashpay/dashd-go/chaincfg"
)

func TestDashAddressNetworks(t *testing.T) {
	// Pin the Dash Core prefixes and BIP32 identifiers across upstream upgrades.
	for _, tc := range []struct {
		params          *chaincfg.Params
		pkh, sh         byte
		private, public [4]byte
	}{
		{&chaincfg.MainNetParams, 0x4c, 0x10, [4]byte{4, 0x88, 0xad, 0xe4}, [4]byte{4, 0x88, 0xb2, 0x1e}},
		{&chaincfg.TestNet3Params, 0x8c, 0x13, [4]byte{4, 0x35, 0x83, 0x94}, [4]byte{4, 0x35, 0x87, 0xcf}},
		{&chaincfg.RegressionNetParams, 0x8c, 0x13, [4]byte{4, 0x35, 0x83, 0x94}, [4]byte{4, 0x35, 0x87, 0xcf}},
	} {
		t.Run(tc.params.Name, func(t *testing.T) {
			if tc.params.PubKeyHashAddrID != tc.pkh || tc.params.ScriptHashAddrID != tc.sh {
				t.Fatal("upstream address prefixes differ from Dash Core")
			}
			if tc.params.HDPrivateKeyID != tc.private || tc.params.HDPublicKeyID != tc.public {
				t.Fatal("upstream HD key prefixes differ from Dash Core")
			}
			pkh, _ := btcutil.NewAddressPubKeyHash(bytes.Repeat([]byte{1}, 20), tc.params)
			sh, _ := btcutil.NewAddressScriptHashFromHash(bytes.Repeat([]byte{2}, 20), tc.params)
			for _, address := range []btcutil.Address{pkh, sh} {
				decoded, err := DecodeAddress(address.String(), tc.params)
				if err != nil {
					t.Fatal(err)
				}
				if decoded.String() != address.String() {
					t.Fatalf("address round trip: got %s, want %s", decoded, address)
				}
			}
		})
	}
}

func TestDashRejectsUnsupportedAddresses(t *testing.T) {
	_, pubKey := btcec.PrivKeyFromBytes([]byte{1})
	witness, _ := btcutil.NewAddressWitnessPubKeyHash(bytes.Repeat([]byte{1}, 20), &chaincfg.MainNetParams)
	testnet, _ := btcutil.NewAddressPubKeyHash(bytes.Repeat([]byte{1}, 20), &chaincfg.TestNet3Params)
	for _, address := range []string{
		witness.String(), testnet.String(), hex.EncodeToString(pubKey.SerializeCompressed()),
		"1BoatSLRHtKNngkdXEeobR76b53LETtpyT", "invalid",
	} {
		_, err := DecodeAddress(address, &chaincfg.MainNetParams)
		if err == nil {
			t.Fatalf("accepted unsupported Dash destination %s", address)
		}
	}
}
