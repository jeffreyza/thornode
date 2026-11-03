package zcash

import (
	"fmt"
	"strings"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcutil"
)

// Address holds the hash and network prefix of a transparent P2PKH or P2SH address.
type Address struct {
	hash       [20]byte
	prefix     [2]byte
	scriptHash bool
}

// addressPrefixes selects Zcash's Base58 prefixes and TEX human-readable prefix.
func addressPrefixes(network string) (pubkey, script [2]byte, tex string, err error) {
	switch network {
	case "mainnet":
		return [2]byte{0x1c, 0xb8}, [2]byte{0x1c, 0xbd}, "tex", nil
	case "testnet3":
		return [2]byte{0x1d, 0x25}, [2]byte{0x1c, 0xba}, "textest", nil
	case "regtest":
		return [2]byte{0x1d, 0x25}, [2]byte{0x1c, 0xba}, "texregtest", nil
	default:
		return pubkey, script, "", fmt.Errorf("unsupported Zcash network %q", network)
	}
}

// NewAddressPubKeyHash constructs a transparent address for a hash160 public key.
func NewAddressPubKeyHash(hash [20]byte, network string) (*Address, error) {
	prefix, _, _, err := addressPrefixes(network)
	if err != nil {
		return nil, err
	}
	return &Address{hash: hash, prefix: prefix}, nil
}

// DecodeAddress validates a transparent or TEX address using btcd's checksum codecs.
func DecodeAddress(text, network string) (*Address, error) {
	pkh, sh, hrp, err := addressPrefixes(network)
	if err != nil {
		return nil, err
	}
	prefix := pkh
	var hash []byte
	if strings.HasPrefix(strings.ToLower(text), "tex") {
		actual, words, version, decodeErr := bech32.DecodeGeneric(text)
		// trunk-ignore(codespell/misspelled): Upstream Bech32m constant.
		if decodeErr != nil || actual != hrp || version != bech32.VersionM {
			return nil, fmt.Errorf("invalid TEX address or network")
		}
		hash, err = bech32.ConvertBits(words, 5, 8, false)
		if err != nil || len(hash) != 20 {
			return nil, fmt.Errorf("invalid TEX address payload")
		}
	} else {
		payload, first, decodeErr := base58.CheckDecode(text)
		if decodeErr != nil || len(payload) != 21 {
			return nil, fmt.Errorf("invalid transparent Zcash address")
		}
		prefix = [2]byte{first, payload[0]}
		if prefix != pkh && prefix != sh {
			return nil, fmt.Errorf("wrong Zcash address network or type")
		}
		hash = payload[1:]
	}
	a := &Address{prefix: prefix, scriptHash: prefix == sh}
	copy(a.hash[:], hash)
	return a, nil
}

// String encodes the two-byte Zcash prefix through btcd's Base58Check encoder.
func (a *Address) String() string {
	payload := append([]byte{a.prefix[1]}, a.hash[:]...)
	return base58.CheckEncode(payload, a.prefix[0])
}

// ScriptAddress returns the public-key or script hash without exposing mutable state.
func (a *Address) ScriptAddress() []byte {
	return append([]byte(nil), a.hash[:]...)
}

// Script returns the standard Bitcoin P2PKH or P2SH script for this Zcash address.
func (a *Address) Script() ([]byte, error) {
	if !a.scriptHash {
		return PayToPubKeyHashScript(a.hash[:])
	}
	// The temporary Bitcoin address selects a script template, never a network address.
	address, err := btcutil.NewAddressScriptHashFromHash(a.hash[:], &chaincfg.Params{})
	if err != nil {
		return nil, err
	}
	return txscript.PayToAddrScript(address)
}

// PayToPubKeyHashScript uses btcd's standard script template, including memo output hashes.
func PayToPubKeyHashScript(hash []byte) ([]byte, error) {
	address, err := btcutil.NewAddressPubKeyHash(hash, &chaincfg.Params{})
	if err != nil {
		return nil, err
	}
	return txscript.PayToAddrScript(address)
}
