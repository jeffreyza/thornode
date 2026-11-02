package common

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcutil"
	"github.com/btcsuite/btcutil/bech32"
	"github.com/cometbft/cometbft/crypto"
	"github.com/cosmos/cosmos-sdk/crypto/codec"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	dashbtcutil "github.com/dashpay/dashd-go/btcutil"
	dashchaincfg "github.com/dashpay/dashd-go/chaincfg"
	dogchaincfg "github.com/eager7/dogd/chaincfg"
	"github.com/eager7/dogutil"
	eth "github.com/ethereum/go-ethereum/crypto"
	bchchaincfg "github.com/gcash/bchd/chaincfg"
	"github.com/gcash/bchutil"
	ltcchaincfg "github.com/ltcsuite/ltcd/chaincfg"
	"github.com/ltcsuite/ltcutil"
	"github.com/mr-tron/base58"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/pivxutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/zcash"
	"golang.org/x/crypto/blake2b"

	adacommon "github.com/blinklabs-io/gouroboros/ledger/common"
	xrpkm "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/xrp/keymanager"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ss58"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// PubKey used in thorchain, it should be bech32 encoded string
// thus it will be something like
// tthorpub1addwnpepqt7qug8vk9r3saw8n4r803ydj2g3dqwx0mvq5akhnze86fc536xcycgtrnv
// tthorpub1addwnpepqdqvd4r84lq9m54m5kk9sf4k6kdgavvch723pcgadulxd6ey9u70k6zq8qe
type (
	PubKey  string
	PubKeys []PubKey
)

var (
	EmptyPubKey            PubKey
	EmptyPubKeySet         PubKeySet
	pubkeyToAddressCache   = make(map[string]Address)
	pubkeyToAddressCacheMu = &sync.RWMutex{}

	// validPubKeyCache holds strings NewPubKey has accepted (failures are never
	// cached). It is bounded by two generations: when cur fills it becomes prev
	// and a fresh cur starts, and prev hits are promoted, so keys still in use
	// survive while junk from query input ages out
	validPubKeyCache   = make(map[string]struct{})
	validPubKeyPrev    = make(map[string]struct{})
	validPubKeyCacheMu sync.RWMutex
)

const validPubKeyCacheMax = 8192

////////////////////////////////////////////////////////////////////////////////////////
// PubKey
////////////////////////////////////////////////////////////////////////////////////////

// DecodePubKey decodes THORChain's account public-key encoding using the
// configured network prefix. It preserves the encoded key's algorithm and
// returns an error for an empty or invalid key.
func DecodePubKey(encoded string) (cryptotypes.PubKey, error) {
	return cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeAccPub, encoded)
}

// NewPubKey create a new instance of PubKey
// key is bech32 encoded string
func NewPubKey(key string) (PubKey, error) {
	if len(key) == 0 {
		return EmptyPubKey, nil
	}
	// validation is a pure function of the string and the account pubkey prefix;
	// vault memberships re-validate the same keys constantly
	cacheKey := cosmos.GetConfig().GetBech32AccountPubPrefix() + "|" + key
	validPubKeyCacheMu.RLock()
	_, ok := validPubKeyCache[cacheKey]
	_, inPrev := validPubKeyPrev[cacheKey]
	validPubKeyCacheMu.RUnlock()
	if ok {
		return PubKey(key), nil
	}
	if !inPrev {
		_, err := DecodePubKey(key)
		if err != nil {
			return EmptyPubKey, fmt.Errorf("%s is not bech32 encoded pub key,err : %w", key, err)
		}
	}
	rememberValidPubKey(cacheKey)
	return PubKey(key), nil
}

func rememberValidPubKey(cacheKey string) {
	validPubKeyCacheMu.Lock()
	defer validPubKeyCacheMu.Unlock()
	if len(validPubKeyCache) >= validPubKeyCacheMax {
		validPubKeyPrev = validPubKeyCache
		validPubKeyCache = make(map[string]struct{}, validPubKeyCacheMax)
	}
	validPubKeyCache[cacheKey] = struct{}{}
}

// NewPubKeyFromCrypto
func NewPubKeyFromCrypto(pk crypto.PubKey) (PubKey, error) {
	tmp, err := codec.FromCmtPubKeyInterface(pk)
	if err != nil {
		return EmptyPubKey, fmt.Errorf("fail to create PubKey from crypto.PubKey,err:%w", err)
	}
	s, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, tmp)
	if err != nil {
		return EmptyPubKey, fmt.Errorf("fail to create PubKey from crypto.PubKey,err:%w", err)
	}
	return PubKey(s), nil
}

// Equals check whether two are the same
func (p PubKey) Equals(pubKey1 PubKey) bool {
	return p == pubKey1
}

// IsEmpty to check whether it is empty
func (p PubKey) IsEmpty() bool {
	return len(p) == 0
}

// String stringer implementation
func (p PubKey) String() string {
	return string(p)
}

// Decode returns the decoded public key, or an error for an empty or invalid key.
func (p PubKey) Decode() (cryptotypes.PubKey, error) {
	return DecodePubKey(p.String())
}

func (p PubKey) Secp256K1() (*btcec.PublicKey, error) {
	pk, err := p.Decode()
	if err != nil {
		return nil, err
	}
	return btcec.ParsePubKey(pk.Bytes(), btcec.S256())
}

// EVMPubkeyToAddress converts a pubkey of an EVM chain to the corresponding address
func (p PubKey) EVMPubkeyToAddress() (Address, error) {
	pub, err := p.Secp256K1()
	if err != nil {
		return NoAddress, err
	}
	str := strings.ToLower(eth.PubkeyToAddress(*pub.ToECDSA()).String())
	return NewAddress(str)
}

// GetAddress will return an address for the given chain
func (p PubKey) GetAddress(chain Chain) (Address, error) {
	if p.IsEmpty() {
		return NoAddress, nil
	}

	// cache pubkey to address, since this is expensive with many vaults in pubkey manager
	key := chain.String() + "-" + p.String()
	pubkeyToAddressCacheMu.RLock()
	if v, ok := pubkeyToAddressCache[key]; ok {
		pubkeyToAddressCacheMu.RUnlock()
		return v, nil
	}
	pubkeyToAddressCacheMu.RUnlock()

	chainNetwork := CurrentChainNetwork
	var addressString string
	switch chain {
	case XRPChain:
		pk, err := p.Secp256K1()
		if err != nil {
			return NoAddress, fmt.Errorf("get pub key secp256k1, %w", err)
		}
		addressString = xrpkm.MasterPubKeyToAccountID(pk.SerializeCompressed())
	case GAIAChain, THORChain, NOBLEChain:
		pk, err := p.Decode()
		if err != nil {
			return NoAddress, err
		}
		str, err := ConvertAndEncode(chain.AddressPrefix(chainNetwork), pk.Address().Bytes())
		if err != nil {
			return NoAddress, fmt.Errorf("fail to bech32 encode the address, err: %w", err)
		}
		addressString = str
	case BTCChain:
		pk, err := p.Decode()
		if err != nil {
			return NoAddress, err
		}
		var net *chaincfg.Params
		switch chainNetwork {
		case MockNet:
			net = &chaincfg.RegressionNetParams
		case MainNet, StageNet, ChainNet:
			net = &chaincfg.MainNetParams
		}
		addr, err := btcutil.NewAddressWitnessPubKeyHash(pk.Address().Bytes(), net)
		if err != nil {
			return NoAddress, fmt.Errorf("fail to bech32 encode the address, err: %w", err)
		}
		addressString = addr.String()
	case LTCChain:
		pk, err := p.Decode()
		if err != nil {
			return NoAddress, err
		}
		var net *ltcchaincfg.Params
		switch chainNetwork {
		case MockNet:
			net = &ltcchaincfg.RegressionNetParams
		case MainNet, StageNet, ChainNet:
			net = &ltcchaincfg.MainNetParams
		}
		addr, err := ltcutil.NewAddressWitnessPubKeyHash(pk.Address().Bytes(), net)
		if err != nil {
			return NoAddress, fmt.Errorf("fail to bech32 encode the address, err: %w", err)
		}
		addressString = addr.String()
	case DOGEChain:
		pk, err := p.Decode()
		if err != nil {
			return NoAddress, err
		}
		var net *dogchaincfg.Params
		switch chainNetwork {
		case MockNet:
			net = &dogchaincfg.RegressionNetParams
		case MainNet, StageNet, ChainNet:
			net = &dogchaincfg.MainNetParams
		}
		addr, err := dogutil.NewAddressPubKeyHash(pk.Address().Bytes(), net)
		if err != nil {
			return NoAddress, fmt.Errorf("fail to encode the address, err: %w", err)
		}
		addressString = addr.String()
	case BCHChain:
		pk, err := p.Decode()
		if err != nil {
			return NoAddress, err
		}
		var net *bchchaincfg.Params
		switch chainNetwork {
		case MockNet:
			net = &bchchaincfg.RegressionNetParams
		case MainNet, StageNet, ChainNet:
			net = &bchchaincfg.MainNetParams
		}
		addr, err := bchutil.NewAddressPubKeyHash(pk.Address().Bytes(), net)
		if err != nil {
			return NoAddress, fmt.Errorf("fail to encode the address, err: %w", err)
		}
		addressString = addr.String()
	case DASHChain:
		pk, err := p.Decode()
		if err != nil {
			return NoAddress, err
		}
		var net *dashchaincfg.Params
		switch chainNetwork {
		case MockNet:
			net = &dashchaincfg.RegressionNetParams
		case MainNet, StageNet, ChainNet:
			net = &dashchaincfg.MainNetParams
		}
		addr, err := dashbtcutil.NewAddressPubKeyHash(pk.Address().Bytes(), net)
		if err != nil {
			return NoAddress, fmt.Errorf("failed to encode the address, err: %w", err)
		}
		addressString = addr.String()
	case PIVXChain:
		pk, err := p.Decode()
		if err != nil {
			return NoAddress, err
		}
		var net *chaincfg.Params
		switch chainNetwork {
		case MockNet:
			net = pivxutil.RegressionNetParams()
		case MainNet, StageNet, ChainNet:
			net = pivxutil.MainNetParams()
		}
		addr, err := btcutil.NewAddressPubKeyHash(pk.Address().Bytes(), net)
		if err != nil {
			return NoAddress, fmt.Errorf("failed to encode the address, err: %w", err)
		}
		addressString = addr.String()
	case SOLChain:
		pk, err := p.Decode()
		if err != nil {
			return NoAddress, err
		}

		pubBz := pk.Bytes()
		if len(pubBz) != 32 {
			return NoAddress, fmt.Errorf("invalid pubkey length %d", len(pubBz))
		}

		// Encode the public key to base58 to get the Solana address
		addressString = base58.Encode(pubBz)
	case TAOChain, DOTChain:
		pk, err := p.Decode()
		if err != nil {
			return NoAddress, err
		}

		pubBz := pk.Bytes()
		if len(pubBz) != 32 {
			return NoAddress, fmt.Errorf("invalid pubkey length %d", len(pubBz))
		}

		addressString, err = ss58.Encode(chain.SS58Prefix(), pubBz)
		if err != nil {
			return NoAddress, fmt.Errorf("fail to SS58 encode address: %w", err)
		}
	case TRONChain:
		pub, err := p.Secp256K1()
		if err != nil {
			return NoAddress, err
		}

		addr := eth.PubkeyToAddress(*pub.ToECDSA()).Bytes()
		addr = append([]byte{65}, addr...)

		sha256sum := func(data []byte) []byte {
			h := sha256.New()
			h.Write(data)
			return h.Sum(nil)
		}

		checksum := sha256sum(sha256sum(addr))

		addressString = base58.Encode(append(addr, checksum[:4]...))
	case XMRChain:
		pk, err := p.Decode()
		if err != nil {
			return NoAddress, err
		}
		addressString, err = deriveXMRAddressFromSpendPubKey(pk.Bytes(), chainNetwork)
		if err != nil {
			return NoAddress, fmt.Errorf("fail to derive monero address from pubkey: %w", err)
		}
	case ZECChain:
		pk, err := p.Decode()
		if err != nil {
			return NoAddress, fmt.Errorf("fail to get pubkey from bech32: %w", err)
		}

		var bz [20]byte
		copy(bz[:], pk.Address().Bytes())

		address, err := zcash.NewAddressPubKeyHash(bz, ZECNetworkName())
		if err != nil {
			return NoAddress, err
		}
		addressString = address.String()
	case SUIChain:
		pk, err := p.Decode()
		if err != nil {
			return NoAddress, err
		}

		pub := append([]byte{0x00}, pk.Bytes()...)

		hash := blake2b.Sum256(pub)

		addressString = "0x" + hex.EncodeToString(hash[:])
	case ADAChain:
		pk, err := p.Decode()
		if err != nil {
			return NoAddress, err
		}

		var network uint8
		switch chainNetwork {
		case MockNet:
			network = adacommon.AddressNetworkTestnet
		default:
			network = adacommon.AddressNetworkMainnet
		}

		addr, err := adacommon.NewAddressFromParts(
			adacommon.AddressTypeKeyNone,
			network,
			pk.Bytes(),
			nil,
		)
		if err != nil {
			return NoAddress, err
		}

		addressString = addr.String()
	default:
		// Only EVM chains remain.
		if !chain.IsEVM() {
			return NoAddress, nil
		}
		addr, err := p.EVMPubkeyToAddress()
		if err != nil {
			return addr, err
		}
		addressString = addr.String()
	}

	address, err := NewAddress(addressString)
	if err != nil {
		return address, fmt.Errorf("NewAddress, addressString %s, %w", addressString, err)
	}
	pubkeyToAddressCacheMu.Lock()
	pubkeyToAddressCache[key] = address
	pubkeyToAddressCacheMu.Unlock()
	return address, nil
}

// thorAddressCacheKey identifies a GetThorAddress result: the decode checks
// the address against the account address prefix of the SDK config.
type thorAddressCacheKey struct {
	prefix string
	pubKey PubKey
}

// thorAddressCache memoizes successful GetThorAddress results. GetAddress
// pins a pubkey's THORChain address for the life of the process
// (pubkeyToAddressCache is never cleared) and decoding it is a pure function
// of that string and the account prefix, so the cache cannot change results.
// Bounded: once full, results are computed without being stored.
var (
	thorAddressCache   = make(map[thorAddressCacheKey]cosmos.AccAddress)
	thorAddressCacheMu sync.RWMutex
)

const thorAddressCacheMax = 8192

// GetThorAddress returns the THORChain account address of the pubkey. The
// returned address is a fresh copy the caller may modify.
func (p PubKey) GetThorAddress() (cosmos.AccAddress, error) {
	key := thorAddressCacheKey{prefix: cosmos.GetConfig().GetBech32AccountAddrPrefix(), pubKey: p}
	thorAddressCacheMu.RLock()
	cached, ok := thorAddressCache[key]
	thorAddressCacheMu.RUnlock()
	if ok {
		return append(cosmos.AccAddress(nil), cached...), nil
	}
	addr, err := p.GetAddress(THORChain)
	if err != nil {
		return nil, err
	}
	accAddr, err := cosmos.AccAddressFromBech32(addr.String())
	if err != nil {
		return accAddr, err
	}
	thorAddressCacheMu.Lock()
	if len(thorAddressCache) < thorAddressCacheMax {
		thorAddressCache[key] = append(cosmos.AccAddress(nil), accAddr...)
	}
	thorAddressCacheMu.Unlock()
	return accAddr, nil
}

// MarshalJSON to Marshals to JSON using Bech32
func (p PubKey) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.String())
}

// UnmarshalJSON to Unmarshal from JSON assuming Bech32 encoding
func (p *PubKey) UnmarshalJSON(data []byte) error {
	var s string
	err := json.Unmarshal(data, &s)
	if err != nil {
		return err
	}
	pk, err := NewPubKey(s)
	if err != nil {
		return err
	}
	*p = pk
	return nil
}

////////////////////////////////////////////////////////////////////////////////////////
// PubKeys
////////////////////////////////////////////////////////////////////////////////////////

func (p PubKeys) Valid() error {
	for _, pk := range p {
		if _, err := NewPubKey(pk.String()); err != nil {
			return err
		}
	}
	return nil
}

func (p PubKeys) Contains(pk PubKey) bool {
	for _, pp := range p {
		if pp.Equals(pk) {
			return true
		}
	}
	return false
}

// Equals check whether two pub keys are identical
func (p PubKeys) Equals(newPks PubKeys) bool {
	if len(p) != len(newPks) {
		return false
	}

	source := make(PubKeys, len(p))
	dest := make(PubKeys, len(newPks))
	copy(source, p)
	copy(dest, newPks)

	// sort both lists
	sort.Slice(source[:], func(i, j int) bool {
		return source[i].String() < source[j].String()
	})
	sort.Slice(dest[:], func(i, j int) bool {
		return dest[i].String() < dest[j].String()
	})
	for i := range source {
		if !source[i].Equals(dest[i]) {
			return false
		}
	}
	return true
}

// String implement stringer interface
func (p PubKeys) String() string {
	strs := make([]string, len(p))
	for i := range p {
		strs[i] = p[i].String()
	}
	return strings.Join(strs, ", ")
}

func (p PubKeys) Strings() []string {
	allStrings := make([]string, len(p))
	for i, pk := range p {
		allStrings[i] = pk.String()
	}
	return allStrings
}

func (p PubKeys) Addresses() ([]cosmos.AccAddress, error) {
	var err error
	addrs := make([]cosmos.AccAddress, len(p))
	for i, pk := range p {
		addrs[i], err = pk.GetThorAddress()
		if err != nil {
			return nil, err
		}
	}
	return addrs, nil
}

// ConvertAndEncode converts from a base64 encoded byte string to hex or base32 encoded byte string and then to bech32
func ConvertAndEncode(hrp string, data []byte) (string, error) {
	converted, err := bech32.ConvertBits(data, 8, 5, true)
	if err != nil {
		return "", fmt.Errorf("encoding bech32 failed,%w", err)
	}
	return bech32.Encode(hrp, converted)
}

////////////////////////////////////////////////////////////////////////////////////////
// PubKeySet
////////////////////////////////////////////////////////////////////////////////////////

// NewPubKeySet create a new instance of PubKeySet , which contains two keys
func NewPubKeySet(secp256k1, ed25519 PubKey) PubKeySet {
	return PubKeySet{
		Secp256k1: secp256k1,
		Ed25519:   ed25519,
	}
}

// IsEmpty will determinate whether PubKeySet is an empty
func (p PubKeySet) IsEmpty() bool {
	return p.Secp256k1.IsEmpty() || p.Ed25519.IsEmpty()
}

// Equals check whether two PubKeySet are the same
func (p PubKeySet) Equals(pks1 PubKeySet) bool {
	return p.Ed25519.Equals(pks1.Ed25519) && p.Secp256k1.Equals(pks1.Secp256k1)
}

func (p PubKeySet) Contains(pk PubKey) bool {
	return p.Ed25519.Equals(pk) || p.Secp256k1.Equals(pk)
}

// String implements fmt.Stringer
func (p PubKeySet) String() string {
	return fmt.Sprintf(`
	secp256k1: %s
	ed25519: %s
`, p.Secp256k1.String(), p.Ed25519.String())
}

// GetAddress
func (p PubKeySet) GetAddress(chain Chain) (Address, error) {
	switch chain.GetSigningAlgo() {
	case SigningAlgoSecp256k1:
		return p.Secp256k1.GetAddress(chain)
	case SigningAlgoEd25519:
		return p.Ed25519.GetAddress(chain)
	}
	return NoAddress, fmt.Errorf("unknown signing algorithm")
}
