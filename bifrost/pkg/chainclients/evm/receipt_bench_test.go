package evm

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	ethclient "github.com/ethereum/go-ethereum/ethclient"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	thorcommon "gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

const benchVaultCount = 50

// newReceiptBenchScanner builds a scanner the way TestDirectERC20TransferFeatureFlag
// does, but with a real PubKeyManager holding benchVaultCount pubkeys. It returns
// the scanner and the pool address of the most recently added (active) vault.
func newReceiptBenchScanner(b *testing.B, disableWhitelist int64) (*EVMScanner, common.Address) {
	b.Helper()
	setupMocknetThorConfig()

	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0xa868"}`))
	}))
	b.Cleanup(server.Close)
	ethClient, err := ethclient.Dial(server.URL)
	if err != nil {
		b.Fatal(err)
	}
	storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = storage.Close() })

	tokenManager, err := evm.NewTokenManager(
		storage.GetInternalDb(),
		"bench-tokenmeta-",
		thorcommon.AVAXAsset,
		defaultDecimals,
		time.Second,
		nil,
		ethClient,
		routerContractABI,
		erc20ContractABI,
	)
	if err != nil {
		b.Fatal(err)
	}
	vaultABI, _, err := evm.GetContractABI(routerContractABI, erc20ContractABI)
	if err != nil {
		b.Fatal(err)
	}

	pkm, err := pubkeymanager.NewPubKeyManager(nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	var vault common.Address
	for i := 0; i < benchVaultCount; i++ {
		priv := secp256k1.GenPrivKeyFromSecret([]byte(fmt.Sprintf("bifrost-bench-vault-%d", i)))
		tm, err := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())
		if err != nil {
			b.Fatal(err)
		}
		pk, err := thorcommon.NewPubKeyFromCrypto(tm)
		if err != nil {
			b.Fatal(err)
		}
		pkm.AddPubKey(pk, false, thorcommon.SigningAlgoSecp256k1)
		addr, err := pk.GetAddress(thorcommon.AVAXChain)
		if err != nil {
			b.Fatal(err)
		}
		vault = common.HexToAddress(addr.String())
	}

	bridge := testMimirBridge{
		getMimir: func(key string) (int64, error) {
			if key == constants.EVMDisableContractWhitelist.String() {
				return disableWhitelist, nil
			}
			return -1, nil
		},
	}

	scanner := &EVMScanner{
		cfg:          getConfigForTest(),
		bridge:       bridge,
		pubkeyMgr:    pkm,
		eipSigner:    etypes.NewPragueSigner(big.NewInt(43112)),
		tokenManager: tokenManager,
		vaultABI:     vaultABI,
	}
	return scanner, vault
}

// signedBenchTx returns the binary encoding of a signed dynamic-fee tx so that
// each benchmark iteration can decode a fresh *Transaction, as the scanner does
// for every block (the go-ethereum sender cache lives on the tx object).
func signedBenchTx(b *testing.B, to common.Address, value *big.Int, data []byte) []byte {
	b.Helper()
	key, err := crypto.HexToECDSA("8a1f9a8f95be41cd7ccb6168179afb4504aefe388d1e14474d32c45c72ce7b7a")
	if err != nil {
		b.Fatal(err)
	}
	chainID := big.NewInt(43112)
	tx, err := etypes.SignNewTx(key, etypes.NewPragueSigner(chainID), &etypes.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     7,
		GasTipCap: big.NewInt(1e9),
		GasFeeCap: big.NewInt(30e9),
		Gas:       100000,
		To:        &to,
		Value:     value,
		Data:      data,
	})
	if err != nil {
		b.Fatal(err)
	}
	raw, err := tx.MarshalBinary()
	if err != nil {
		b.Fatal(err)
	}
	return raw
}

func decodeBenchTx(b *testing.B, raw []byte) *etypes.Transaction {
	tx := new(etypes.Transaction)
	if err := tx.UnmarshalBinary(raw); err != nil {
		b.Fatal(err)
	}
	return tx
}

func BenchmarkReceiptToTxInItem(b *testing.B) {
	memo := []byte(hex.EncodeToString([]byte("=:THOR.RUNE:tthor1qkd5f9xh2g87wmjc620uf5w08ygdx4etu0u9fs")))
	value := new(big.Int).Mul(big.NewInt(5), big.NewInt(1e18))
	receipt := &etypes.Receipt{
		Status:            1,
		GasUsed:           21000,
		EffectiveGasPrice: big.NewInt(25e9),
	}

	// Unrelated contract call (e.g. a DEX swap) emitting a log from an
	// unknown contract: the bulk of mainnet traffic on the whitelist-disabled path.
	otherContract := common.HexToAddress("0x1111111254EEB25477B68fb85Ed929f73A960582")
	callData := make([]byte, 4+64)
	copy(callData, crypto.Keccak256([]byte("swap(address,uint256)"))[:4])
	contractReceipt := &etypes.Receipt{
		Status:            1,
		GasUsed:           120000,
		EffectiveGasPrice: big.NewInt(25e9),
		Logs: []*etypes.Log{{
			Address: common.HexToAddress("0x2222222222222222222222222222222222222222"),
			Topics:  []common.Hash{common.HexToHash(testERC20TransferEvent)},
			Data:    make([]byte, 32),
		}},
	}

	b.Run("NativeToVault/whitelist", func(b *testing.B) {
		scanner, vault := newReceiptBenchScanner(b, 0)
		raw := signedBenchTx(b, vault, value, memo)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			item, err := scanner.receiptToTxInItem(decodeBenchTx(b, raw), receipt)
			if err != nil || item == nil {
				b.Fatalf("unexpected result: %v %v", item, err)
			}
		}
	})

	b.Run("NativeNonVault/whitelist", func(b *testing.B) {
		scanner, _ := newReceiptBenchScanner(b, 0)
		raw := signedBenchTx(b, common.HexToAddress("0x3333333333333333333333333333333333333333"), value, nil)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			item, err := scanner.receiptToTxInItem(decodeBenchTx(b, raw), receipt)
			if err != nil || item != nil {
				b.Fatalf("unexpected result: %v %v", item, err)
			}
		}
	})

	b.Run("ContractCall/nowhitelist", func(b *testing.B) {
		scanner, _ := newReceiptBenchScanner(b, 1)
		raw := signedBenchTx(b, otherContract, big.NewInt(0), callData)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			item, err := scanner.receiptToTxInItem(decodeBenchTx(b, raw), contractReceipt)
			if err != nil || item != nil {
				b.Fatalf("unexpected result: %v %v", item, err)
			}
		}
	})

	// Decode cost alone, included in every case above, for reference.
	b.Run("DecodeOnly", func(b *testing.B) {
		raw := signedBenchTx(b, otherContract, big.NewInt(0), callData)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = decodeBenchTx(b, raw)
		}
	})
}
