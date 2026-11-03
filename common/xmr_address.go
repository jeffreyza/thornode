package common

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"filippo.io/edwards25519"
	"github.com/btcsuite/btcutil/bech32"
	eth "github.com/ethereum/go-ethereum/crypto"
)

const (
	// Standard Monero network/type bytes.
	xmrPrimaryNetworkByte    byte = 0x12 // standard address, starts with "4"
	xmrIntegratedNetworkByte byte = 0x13 // integrated address, starts with "4"
	xmrSubaddressNetworkByte byte = 0x2a // subaddress, starts with "8"

	// Featured address network/type bytes.
	xmrFeaturedMainnetNetworkByte  byte = 0x46 // starts with "C"
	xmrFeaturedStagenetNetworkByte byte = 0x56 // starts with "F"
	xmrFeaturedTestnetNetworkByte  byte = 0x6f // starts with "K"

	// Featured address bit layout.
	xmrFeaturedSubaddressBit uint64 = 1 << 0
	xmrFeaturedIntegratedBit uint64 = 1 << 1
	xmrFeaturedGuaranteedBit uint64 = 1 << 2

	xmrVarintContinuationMask byte = 0x80
	xmrMaxVarintBytes              = 10
	xmrUnknownSenderHRP            = "xmrunknown"
)

var (
	xmrBase58Alphabet = []byte("123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz")
	xmrBase58Radix    = big.NewInt(58)
	xmrEncodedBlockSz = [...]int{0, 2, 3, 5, 6, 7, 9, 10, 11}

	xmrViewPubKeyOnce sync.Once
	xmrViewPubKey     [32]byte
	xmrViewPubKeyErr  error

	xmrViewScalarOnce sync.Once
	xmrViewScalarHex  string
	xmrViewScalarErr  error

	// Reverse lookups populated by init().
	xmrBase58CharMap  [256]int // byte value → alphabet index (-1 = invalid)
	xmrDecodedBlockSz [12]int  // encoded block length → raw block length
)

type xmrAddressInfo struct {
	networkByte        byte
	isFeatured         bool
	featuredSubaddress bool
	featuredIntegrated bool
	featuredGuaranteed bool
}

// ObservedSenderInfo models whether an observed inbound sender address is
// usable as a real refund/counterparty address. This lets higher layers reason
// about "sender unavailable" explicitly instead of branching on marker
// addresses directly.
type ObservedSenderInfo struct {
	Address               Address
	Known                 bool
	RefundAddressRequired bool
}

func init() {
	for i := range xmrBase58CharMap {
		xmrBase58CharMap[i] = -1
	}
	for i, c := range xmrBase58Alphabet {
		xmrBase58CharMap[c] = i
	}
	for rawSz, encSz := range xmrEncodedBlockSz {
		if encSz > 0 && encSz < len(xmrDecodedBlockSz) {
			xmrDecodedBlockSz[encSz] = rawSz
		}
	}
}

func deriveXMRAddressFromSpendPubKey(spendPubKey []byte, chainNetwork ChainNetwork) (string, error) {
	if len(spendPubKey) != ed25519.PublicKeySize {
		return "", fmt.Errorf("invalid monero spend pubkey length: %d", len(spendPubKey))
	}
	if !isValidXMRPubKey(spendPubKey) {
		return "", fmt.Errorf("invalid monero spend pubkey")
	}
	_ = chainNetwork

	viewPubKey, err := deriveXMRViewPubKey()
	if err != nil {
		return "", err
	}

	payload := make([]byte, 0, 1+32+32+4)
	payload = append(payload, xmrPrimaryNetworkByte)
	payload = append(payload, spendPubKey...)
	payload = append(payload, viewPubKey[:]...)

	checksum := eth.Keccak256(payload)
	payload = append(payload, checksum[:4]...)

	return xmrBase58Encode(payload)
}

func deriveXMRViewPubKey() ([32]byte, error) {
	xmrViewPubKeyOnce.Do(func() {
		var k [8]byte
		binary.LittleEndian.PutUint64(k[:], 0)

		hasher := sha512.New()
		hasher.Write([]byte("THORChain View Key"))
		hasher.Write(k[:])
		sum := hasher.Sum(nil)

		scalar, err := new(edwards25519.Scalar).SetUniformBytes(sum)
		if err != nil {
			xmrViewPubKeyErr = fmt.Errorf("fail to derive monero additional view scalar: %w", err)
			return
		}
		point := new(edwards25519.Point).ScalarBaseMult(scalar)
		copy(xmrViewPubKey[:], point.Bytes())
	})
	return xmrViewPubKey, xmrViewPubKeyErr
}

func isValidXMRPubKey(key []byte) bool {
	if len(key) != ed25519.PublicKeySize {
		return false
	}
	point, err := new(edwards25519.Point).SetBytes(key)
	if err != nil {
		return false
	}
	return new(edwards25519.Point).MultByCofactor(point).Equal(edwards25519.NewIdentityPoint()) != 1
}

func xmrDeterministicPubKey(seed []byte) [32]byte {
	hasher := sha512.New()
	hasher.Write([]byte("THORChain XMR deterministic pubkey"))
	hasher.Write(seed)
	sum := hasher.Sum(nil)

	scalar, err := new(edwards25519.Scalar).SetUniformBytes(sum)
	if err != nil {
		return [32]byte{}
	}
	point := new(edwards25519.Point).ScalarBaseMult(scalar)
	var out [32]byte
	copy(out[:], point.Bytes())
	return out
}

func XMRViewKeyHex() (string, error) {
	xmrViewScalarOnce.Do(func() {
		var k [8]byte
		binary.LittleEndian.PutUint64(k[:], 0)

		hasher := sha512.New()
		hasher.Write([]byte("THORChain View Key"))
		hasher.Write(k[:])
		sum := hasher.Sum(nil)

		scalar, err := new(edwards25519.Scalar).SetUniformBytes(sum)
		if err != nil {
			xmrViewScalarErr = fmt.Errorf("fail to derive monero view scalar: %w", err)
			return
		}
		xmrViewScalarHex = fmt.Sprintf("%x", scalar.Bytes())
	})
	return xmrViewScalarHex, xmrViewScalarErr
}

// ---------------------------------------------------------------------------
// Monero base58 encode/decode
// ---------------------------------------------------------------------------

func xmrBase58Encode(data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}

	out := make([]byte, 0, ((len(data)+7)/8)*11)
	for start := 0; start < len(data); start += 8 {
		end := start + 8
		if end > len(data) {
			end = len(data)
		}
		block := data[start:end]
		encodedSize := xmrEncodedBlockSz[len(block)]
		if encodedSize == 0 {
			return "", fmt.Errorf("invalid monero base58 block size: %d", len(block))
		}
		encodedBlock, err := xmrBase58EncodeBlock(block, encodedSize)
		if err != nil {
			return "", err
		}
		out = append(out, encodedBlock...)
	}

	return string(out), nil
}

func xmrBase58EncodeBlock(block []byte, encodedSize int) ([]byte, error) {
	value := new(big.Int).SetBytes(block)
	encoded := make([]byte, encodedSize)
	for i := range encoded {
		encoded[i] = xmrBase58Alphabet[0]
	}

	mod := new(big.Int)
	for i := encodedSize - 1; i >= 0 && value.Sign() > 0; i-- {
		value.DivMod(value, xmrBase58Radix, mod)
		encoded[i] = xmrBase58Alphabet[mod.Int64()]
	}
	if value.Sign() != 0 {
		return nil, fmt.Errorf("monero base58 block overflow")
	}

	return encoded, nil
}

// xmrBase58Decode decodes a Monero base58-encoded string back to raw bytes.
func xmrBase58Decode(encoded string) ([]byte, error) {
	if len(encoded) == 0 {
		return nil, nil
	}

	var out []byte
	for start := 0; start < len(encoded); start += 11 {
		end := start + 11
		if end > len(encoded) {
			end = len(encoded)
		}
		block := encoded[start:end]
		rawSize := xmrDecodedBlockSz[len(block)]
		if rawSize == 0 {
			return nil, fmt.Errorf("invalid monero base58 encoded block size: %d", len(block))
		}
		decoded, err := xmrBase58DecodeBlock(block, rawSize)
		if err != nil {
			return nil, err
		}
		out = append(out, decoded...)
	}

	return out, nil
}

func xmrBase58DecodeBlock(block string, rawSize int) ([]byte, error) {
	value := new(big.Int)
	for i := 0; i < len(block); i++ {
		idx := xmrBase58CharMap[block[i]]
		if idx < 0 {
			return nil, fmt.Errorf("invalid monero base58 character: %c", block[i])
		}
		value.Mul(value, xmrBase58Radix)
		value.Add(value, big.NewInt(int64(idx)))
	}

	raw := value.Bytes()
	if len(raw) > rawSize {
		return nil, fmt.Errorf("monero base58 block overflow")
	}

	// Left-pad with zeros to rawSize.
	if len(raw) < rawSize {
		padded := make([]byte, rawSize)
		copy(padded[rawSize-len(raw):], raw)
		raw = padded
	}

	return raw, nil
}

// ---------------------------------------------------------------------------
// Address validation
// ---------------------------------------------------------------------------

// xmrAddressValid decodes a Monero address and validates its checksum and network byte.
func xmrAddressValid(address string) bool {
	_, ok := xmrDecodeAddressInfo(address)
	return ok
}

func xmrAddressInfoFromString(address string) (xmrAddressInfo, bool) {
	return xmrDecodeAddressInfo(address)
}

func xmrDecodeAddressInfo(address string) (xmrAddressInfo, bool) {
	info := xmrAddressInfo{}
	raw, err := xmrBase58Decode(address)
	if err != nil {
		return info, false
	}

	// All supported XMR payloads are:
	// - Standard/subaddress: 1 + 32 + 32 + 4  = 69
	// - Integrated:          1 + 32 + 32 + 8 + 4 = 77
	// - Featured:            1 + 32 + 32 + varint + [optional 8] + 4
	if len(raw) < 69 {
		return info, false
	}

	// Verify Keccak-256 checksum (last 4 bytes).
	checksumOffset := len(raw) - 4
	actualChecksum := eth.Keccak256(raw[:checksumOffset])
	if !bytes.Equal(raw[checksumOffset:], actualChecksum[:4]) {
		return info, false
	}

	body := raw[:checksumOffset]
	if len(body) < 1+32+32 {
		return info, false
	}
	if !isValidXMRPubKey(body[1:33]) || !isValidXMRPubKey(body[33:65]) {
		return info, false
	}

	info.networkByte = body[0]
	rest := body[1+32+32:]
	switch info.networkByte {
	case xmrPrimaryNetworkByte, xmrSubaddressNetworkByte:
		if len(rest) != 0 {
			return info, false
		}
		return info, true
	case xmrIntegratedNetworkByte:
		if len(rest) != 8 {
			return info, false
		}
		return info, true
	case xmrFeaturedMainnetNetworkByte, xmrFeaturedStagenetNetworkByte, xmrFeaturedTestnetNetworkByte:
		features, consumed, ok := xmrReadVarint(rest)
		if !ok {
			return info, false
		}
		if (features >> 3) != 0 {
			return info, false
		}
		info.isFeatured = true
		info.featuredSubaddress = (features & xmrFeaturedSubaddressBit) == xmrFeaturedSubaddressBit
		info.featuredIntegrated = (features & xmrFeaturedIntegratedBit) == xmrFeaturedIntegratedBit
		info.featuredGuaranteed = (features & xmrFeaturedGuaranteedBit) == xmrFeaturedGuaranteedBit
		rest = rest[consumed:]
		if info.featuredIntegrated {
			if len(rest) != 8 {
				return info, false
			}
		} else if len(rest) != 0 {
			return info, false
		}
		return info, true
	default:
		return info, false
	}
}

// XMRChecksummedDummyAddress generates a valid XMR address with deterministic keys for the
// given prefix ('4' for primary, '8' for subaddress, 'C' for featured+guaranteed).
func XMRChecksummedDummyAddress(prefix byte) string {
	spendKey := xmrDeterministicPubKey([]byte{prefix, 's'})
	viewKey := xmrDeterministicPubKey([]byte{prefix, 'v'})

	payload := make([]byte, 0, 1+32+32+2+4)
	switch prefix {
	case '4':
		payload = append(payload, xmrPrimaryNetworkByte)
	case '8':
		payload = append(payload, xmrSubaddressNetworkByte)
	default:
		payload = append(payload, xmrFeaturedMainnetNetworkByte)
		payload = append(payload, spendKey[:]...)
		payload = append(payload, viewKey[:]...)
		features := xmrFeaturedSubaddressBit | xmrFeaturedGuaranteedBit
		payload = xmrAppendVarint(payload, features)
		checksum := eth.Keccak256(payload)
		payload = append(payload, checksum[:4]...)
		addr, _ := xmrBase58Encode(payload)
		return addr
	}
	payload = append(payload, spendKey[:]...)
	payload = append(payload, viewKey[:]...)
	checksum := eth.Keccak256(payload)
	payload = append(payload, checksum[:4]...)
	addr, _ := xmrBase58Encode(payload)
	return addr
}

// XMRSeededDummyAddress generates a valid XMR address deterministically seeded
// by the given data, so that different inputs always produce distinct addresses.
func XMRSeededDummyAddress(prefix byte, seed []byte) string {
	spendKey := xmrDeterministicPubKey(append([]byte("xmr-dummy-spend:"), seed...))
	viewKey := xmrDeterministicPubKey(append([]byte("xmr-dummy-view:"), seed...))

	payload := make([]byte, 0, 1+32+32+2+4)
	switch prefix {
	case '4':
		payload = append(payload, xmrPrimaryNetworkByte)
	case '8':
		payload = append(payload, xmrSubaddressNetworkByte)
	default:
		payload = append(payload, xmrFeaturedMainnetNetworkByte)
		payload = append(payload, spendKey[:]...)
		payload = append(payload, viewKey[:]...)
		features := xmrFeaturedSubaddressBit | xmrFeaturedGuaranteedBit
		payload = xmrAppendVarint(payload, features)
		checksum := eth.Keccak256(payload)
		payload = append(payload, checksum[:4]...)
		addr, _ := xmrBase58Encode(payload)
		return addr
	}
	payload = append(payload, spendKey[:]...)
	payload = append(payload, viewKey[:]...)
	checksum := eth.Keccak256(payload)
	payload = append(payload, checksum[:4]...)
	addr, _ := xmrBase58Encode(payload)
	return addr
}

// XMRUnknownSenderAddress returns a deterministic marker address used when the
// real Monero sender cannot be recovered from chain data.
func XMRUnknownSenderAddress(txID, to string) string {
	material := strings.TrimSpace(txID) + "|" + strings.TrimSpace(to)
	seed := sha256.Sum256([]byte(material))
	data, err := bech32.ConvertBits(seed[:20], 8, 5, true)
	if err != nil {
		return ""
	}
	addr, err := bech32.Encode(xmrUnknownSenderHRP, data)
	if err != nil {
		return ""
	}
	return addr
}

// IsXMRUnknownSenderAddress returns true when addr is an XMR unknown-sender marker.
func IsXMRUnknownSenderAddress(addr Address) bool {
	if addr.IsEmpty() {
		return false
	}
	hrp, _, err := bech32.Decode(strings.ToLower(addr.String()))
	return err == nil && hrp == xmrUnknownSenderHRP
}

// ResolveObservedSender normalizes chain-specific sender availability into an
// explicit runtime model.
func ResolveObservedSender(chain Chain, addr Address) ObservedSenderInfo {
	info := ObservedSenderInfo{
		Address: addr,
		Known:   !addr.IsEmpty(),
	}
	if chain.RequiresExplicitRefundAddress() && IsXMRUnknownSenderAddress(addr) {
		info.Address = NoAddress
		info.Known = false
		info.RefundAddressRequired = true
	}
	return info
}

func xmrAppendVarint(dst []byte, value uint64) []byte {
	for {
		b := byte(value & uint64(^xmrVarintContinuationMask))
		value >>= 7
		if value != 0 {
			b |= xmrVarintContinuationMask
		}
		dst = append(dst, b)
		if value == 0 {
			return dst
		}
	}
}

func xmrReadVarint(data []byte) (uint64, int, bool) {
	var res uint64
	var bits uint
	for i := 0; i < len(data) && i < xmrMaxVarintBytes; i++ {
		b := data[i]
		if bits != 0 && b == 0 {
			return 0, 0, false
		}
		if bits+7 >= 64 && b >= (1<<(64-bits)) {
			return 0, 0, false
		}

		res += uint64(b&^xmrVarintContinuationMask) << bits
		bits += 7
		if (b & xmrVarintContinuationMask) == 0 {
			return res, i + 1, true
		}
	}
	return 0, 0, false
}

// IsXMRFeaturedGuaranteedAddress returns true when address is a valid Monero
// featured address with the guaranteed flag set.
func IsXMRFeaturedGuaranteedAddress(address string) bool {
	info, ok := xmrDecodeAddressInfo(address)
	return ok && info.isFeatured && info.featuredGuaranteed
}

// IsXMRFeaturedAddress returns true when address is a valid Monero featured
// address, regardless of its guaranteed flag.
func IsXMRFeaturedAddress(address string) bool {
	info, ok := xmrDecodeAddressInfo(address)
	return ok && info.isFeatured
}

// IsSupportedXMRAddress returns true when address is structurally valid and
// uses the Monero address variants THORChain supports for external recipients.
func IsSupportedXMRAddress(address string) bool {
	info, ok := xmrAddressInfoFromString(address)
	if !ok {
		return false
	}
	if info.isFeatured {
		return false
	}
	return info.networkByte == xmrPrimaryNetworkByte || info.networkByte == xmrSubaddressNetworkByte
}

func isSupportedFeaturedNetworkByte(networkByte byte) bool {
	return networkByte == xmrFeaturedMainnetNetworkByte
}
