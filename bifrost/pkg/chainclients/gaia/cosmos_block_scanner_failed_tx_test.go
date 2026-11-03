package gaia

import (
	"strings"
	"testing"

	sdkmath "cosmossdk.io/math"
	tmtypes "github.com/cometbft/cometbft/types"
	ctypes "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	protov2 "google.golang.org/protobuf/proto"

	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

type failedTxPoolAddressValidator struct {
	*pubkeymanager.MockPoolAddressValidator
	validAddr string
	info      common.ChainPoolInfo
}

func (v *failedTxPoolAddressValidator) IsValidPoolAddress(addr string, chain common.Chain) (bool, common.ChainPoolInfo) {
	if addr == v.validAddr && chain.Equals(v.info.Chain) {
		return true, v.info
	}
	return false, common.EmptyChainPoolInfo
}

type failedSendTx struct {
	msgs []ctypes.Msg
	fee  ctypes.Coins
	gas  uint64
}

func (m *failedSendTx) GetMsgs() []ctypes.Msg { return m.msgs }
func (m *failedSendTx) GetMsgsV2() ([]protov2.Message, error) {
	return nil, nil
}
func (m *failedSendTx) ValidateBasic() error { return nil }
func (m *failedSendTx) GetGas() uint64       { return m.gas }
func (m *failedSendTx) GetFee() ctypes.Coins { return m.fee }
func (m *failedSendTx) FeePayer() []byte     { return nil }
func (m *failedSendTx) FeeGranter() []byte   { return nil }

func newFailedTxScanner(t *testing.T, validAddr string) *CosmosBlockScanner {
	t.Helper()

	return &CosmosBlockScanner{
		cfg: config.BifrostBlockScannerConfiguration{
			ChainID: common.GAIAChain,
			WhitelistCosmosAssets: []config.WhitelistCosmosAsset{
				{Denom: "uatom", Decimals: 6, THORChainSymbol: "ATOM"},
			},
		},
		pubkeyMgr: &failedTxPoolAddressValidator{
			MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(),
			validAddr:                validAddr,
			info: common.ChainPoolInfo{
				Chain:       common.GAIAChain,
				PubKey:      common.PubKey("mock-pubkey"),
				PoolAddress: common.Address(validAddr),
			},
		},
	}
}

func TestGetTxInFromFailedTransactionIgnoresNonVaultSenders(t *testing.T) {
	t.Parallel()

	scanner := newFailedTxScanner(t, "cosmos1vaultsender")
	tx := &failedSendTx{
		msgs: []ctypes.Msg{
			&banktypes.MsgSend{
				FromAddress: "cosmos1nontreasurysender",
				ToAddress:   "cosmos1receiver",
				Amount:      ctypes.NewCoins(ctypes.NewCoin("uatom", sdkmath.NewInt(1))),
			},
		},
		fee: ctypes.NewCoins(ctypes.NewCoin("uatom", sdkmath.NewInt(1234))),
		gas: GasLimit,
	}

	item := scanner.getTxInFromFailedTransaction("deadbeef", 42, tmtypes.Tx("ignored"), func([]byte) (ctypes.Tx, error) {
		return tx, nil
	})
	if item != nil {
		t.Fatalf("expected non-vault sender to be ignored, got %#v", item)
	}
}

func TestGetTxInFromFailedTransactionBuildsGasObservation(t *testing.T) {
	t.Parallel()

	vaultAddr := "cosmos1vaultsender"
	scanner := newFailedTxScanner(t, vaultAddr)
	tx := &failedSendTx{
		msgs: []ctypes.Msg{
			&banktypes.MsgSend{
				FromAddress: vaultAddr,
				ToAddress:   "cosmos1receiver",
				Amount:      ctypes.NewCoins(ctypes.NewCoin("uatom", sdkmath.NewInt(1))),
			},
		},
		fee: ctypes.NewCoins(ctypes.NewCoin("uatom", sdkmath.NewInt(1234))),
		gas: GasLimit,
	}

	hash := "deadbeef"
	item := scanner.getTxInFromFailedTransaction(hash, 42, tmtypes.Tx("ignored"), func([]byte) (ctypes.Tx, error) {
		return tx, nil
	})
	if item == nil {
		t.Fatal("expected failed vault outbound to produce a gas observation")
	}
	if item.Memo != memo.NewOutboundMemo(common.TxID(strings.ToUpper(hash))).String() {
		t.Fatalf("expected uppercase outbound memo, got %s", item.Memo)
	}
	if item.Sender != vaultAddr {
		t.Fatalf("expected sender %s, got %s", vaultAddr, item.Sender)
	}
	if item.To != "cosmos1receiver" {
		t.Fatalf("expected receiver to be preserved, got %s", item.To)
	}
	gasCoin := item.Gas.ToCoins().GetCoin(common.GAIAChain.GetGasAsset())
	if gasCoin.IsEmpty() {
		t.Fatalf("expected gas coin for %s", common.GAIAChain.GetGasAsset())
	}
	if gasCoin.Amount.Uint64() != 123_400 {
		t.Fatalf("expected gas fee 123400 thorchain units, got %s", gasCoin.Amount.String())
	}
	if item.Coins.GetCoin(common.GAIAChain.GetGasAsset()).Amount.Uint64() != 1 {
		t.Fatalf("expected synthetic gas-only coin amount 1, got %s", item.Coins.GetCoin(common.GAIAChain.GetGasAsset()).Amount.String())
	}
}
