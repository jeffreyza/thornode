package zcash

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/bech32"
)

// TestDecodeAddress preserves existing address vectors and their payment scripts.
func TestDecodeAddress(t *testing.T) {
	for _, test := range []struct{ text, network, hash string }{
		{"tmF834qorixnCV18bVrkM8WN1Xasy5eXcZV", "testnet3", "3b535da0ba90dad71ea005cccfe3cca47d746b3a"},
		{"tmRfZVuDK6gVDfwJie1zepKjAELqaGAgWZr", "testnet3", "aefaebf9c83deba2ec76e080e2cec850dec161b1"},
	} {
		t.Run(test.text, func(t *testing.T) {
			a, err := DecodeAddress(test.text, test.network)
			if err != nil {
				t.Fatal(err)
			}
			script, err := a.Script()
			if err != nil || hex.EncodeToString(script) != "76a914"+test.hash+"88ac" {
				t.Fatal("payment script changed", err)
			}
			if a.String() != test.text || hex.EncodeToString(a.ScriptAddress()) != test.hash {
				t.Fatal("address roundtrip changed")
			}
			hash := a.ScriptAddress()
			hash[0] ^= 1
			if a.String() != test.text {
				t.Fatal("mutable address hash exposed")
			}
			constructed, err := NewAddressPubKeyHash(a.hash, test.network)
			if err != nil || constructed.String() != test.text {
				t.Fatal("address derivation changed", err)
			}
		})
	}
	pkh, err := DecodeAddress("t1XtsHnj4Ev6CWC3HfJ7Xu3GkEP7SCy8hxV", "mainnet")
	if err != nil {
		t.Fatal(err)
	}
	tex, err := DecodeAddress("tex1n88w7cmg9uzdluuct3krjqlkcxyz8tku8sq40s", "mainnet")
	if err != nil || tex.String() != pkh.String() {
		t.Fatal("TEX recipient differs from t1", err)
	}
}

// TestAddressNetworks checks transparent/TEX network boundaries and P2SH script selection.
func TestAddressNetworks(t *testing.T) {
	hash := bytes.Repeat([]byte{0xab}, 20)
	for _, test := range []struct {
		network, hrp string
		pkh, sh      [2]byte
	}{
		{"mainnet", "tex", [2]byte{0x1c, 0xb8}, [2]byte{0x1c, 0xbd}},
		{"testnet3", "textest", [2]byte{0x1d, 0x25}, [2]byte{0x1c, 0xba}},
		{"regtest", "texregtest", [2]byte{0x1d, 0x25}, [2]byte{0x1c, 0xba}},
	} {
		t.Run(test.network, func(t *testing.T) {
			payload := append([]byte{test.sh[1]}, hash...)
			text := base58.CheckEncode(payload, test.sh[0])
			a, err := DecodeAddress(text, test.network)
			if err != nil {
				t.Fatal(err)
			}
			script, err := a.Script()
			expectedScript := "a914" + hex.EncodeToString(hash) + "87"
			if err != nil || hex.EncodeToString(script) != expectedScript || a.String() != text {
				t.Fatal("wrong P2SH script", err)
			}
			words, err := bech32.ConvertBits(hash, 8, 5, true)
			if err != nil {
				t.Fatal(err)
			}
			text, err = bech32.EncodeM(test.hrp, words)
			if err != nil {
				t.Fatal(err)
			}
			for _, spelling := range []string{text, strings.ToUpper(text)} {
				a, err = DecodeAddress(spelling, test.network)
				if err != nil || a.prefix != test.pkh || a.scriptHash || !bytes.Equal(a.ScriptAddress(), hash) {
					t.Fatal("invalid TEX decoding", err)
				}
			}
			for _, wrongNetwork := range []string{"mainnet", "testnet3", "regtest"} {
				if wrongNetwork == test.network {
					continue
				}
				if _, err = DecodeAddress(text, wrongNetwork); err == nil {
					t.Fatal("accepted wrong TEX network")
				}
			}
			legacy, err := bech32.Encode(test.hrp, words)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = DecodeAddress(legacy, test.network); err == nil {
				t.Fatal("accepted Bech32 instead of Bech32m")
			}
		})
	}
	invalidAddresses := []string{
		"", "invalid",
		"t1XtsHnj4Ev6CWC3HfJ7Xu3GkEP7SCy8hxW",
		"tmF834qorixnCV18bVrkM8WN1Xasy5eXcZV",
		"tEx1n88w7cmg9uzdluuct3krjqlkcxyz8tku8sq40s",
	}
	for _, text := range invalidAddresses {
		if _, err := DecodeAddress(text, "mainnet"); err == nil {
			t.Fatal("accepted invalid address", text)
		}
	}
	if _, err := NewAddressPubKeyHash([20]byte{}, "unknown"); err == nil {
		t.Fatal("accepted unknown network")
	}
	if _, err := PayToPubKeyHashScript(hash[:19]); err == nil {
		t.Fatal("accepted short hash")
	}
}
