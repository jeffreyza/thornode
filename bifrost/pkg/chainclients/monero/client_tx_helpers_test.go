package monero

import (
	"errors"
	"strings"
	"testing"

	"go.uber.org/atomic"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// --- Amount / unit conversion ---

func TestThorchainAmountToAtomicBoundary(t *testing.T) {
	t.Parallel()

	maxThorAmount := ^uint64(0) / moneroAtomicToThorchain
	atomic, err := thorchainAmountToAtomic(cosmos.NewUint(maxThorAmount))
	if err != nil {
		t.Fatalf("unexpected conversion error: %v", err)
	}

	expected := maxThorAmount * moneroAtomicToThorchain
	if atomic != expected {
		t.Fatalf("expected atomic amount %d, got %d", expected, atomic)
	}
}

func TestThorchainAmountToAtomicOverflow(t *testing.T) {
	t.Parallel()

	maxThorAmount := ^uint64(0) / moneroAtomicToThorchain
	_, err := thorchainAmountToAtomic(cosmos.NewUint(maxThorAmount + 1))
	if err == nil {
		t.Fatal("expected overflow conversion error")
	}
	if !strings.Contains(err.Error(), "overflows uint64") {
		t.Fatalf("expected overflow error, got %v", err)
	}
}

func TestThorchainAmountToAtomicNil(t *testing.T) {
	t.Parallel()

	var amount cosmos.Uint
	_, err := thorchainAmountToAtomic(amount)
	if err == nil {
		t.Fatal("expected nil amount conversion error")
	}
	if !strings.Contains(err.Error(), "is nil") {
		t.Fatalf("expected nil amount error, got %v", err)
	}
}

func TestEnforceXMRMaxGas(t *testing.T) {
	t.Parallel()

	if err := enforceXMRMaxGas(101, 100); err == nil {
		t.Fatal("expected over-cap fee to be rejected")
	} else if !strings.Contains(err.Error(), tssp.FrostFailReasonXMRMaxGasExceeded) {
		t.Fatalf("unexpected error: %v", err)
	} else {
		var maxGasErr xmrMaxGasExceededError
		if !errors.As(err, &maxGasErr) {
			t.Fatalf("expected typed max gas error, got %T", err)
		}
		if maxGasErr.actualFeeAtomic != 101 || maxGasErr.maxGasAtomic != 100 {
			t.Fatalf("unexpected max gas fields: %+v", maxGasErr)
		}
	}
	if err := enforceXMRMaxGas(100, 100); err != nil {
		t.Fatalf("expected fee equal to cap to pass: %v", err)
	}
	if err := enforceXMRMaxGas(101, 0); err != nil {
		t.Fatalf("expected zero cap to preserve legacy behavior: %v", err)
	}
}

func TestCoinbaseThorchainConversion(t *testing.T) {
	t.Parallel()

	expected := uint64(defaultCoinbaseValue / moneroAtomicToThorchain)
	if expected == 0 {
		expected = 1
	}
	if got := coinbaseThorchain(); got != expected {
		t.Fatalf("expected coinbase thorchain %d, got %d", expected, got)
	}
}

// --- Confirmation counting ---

func TestConfirmationRequiredUsesThorchainUnits(t *testing.T) {
	t.Parallel()

	coinbaseTC := coinbaseThorchain()
	if coinbaseTC == 0 {
		t.Fatal("coinbase thorchain must be non-zero")
	}
	confValue := cosmos.NewUint(coinbaseTC)

	if got := confirmationRequired(cosmos.NewUint(coinbaseTC), confValue); got != 1 {
		t.Fatalf("expected one confirmation at coinbase-sized value, got %d", got)
	}
	if got := confirmationRequired(cosmos.NewUint(coinbaseTC-1), confValue); got != 0 {
		t.Fatalf("expected zero confirmations below coinbase-sized value, got %d", got)
	}
	if got := confirmationRequired(cosmos.ZeroUint(), confValue); got != 0 {
		t.Fatalf("expected zero confirmations for zero value, got %d", got)
	}
	if got := confirmationRequired(cosmos.NewUint(coinbaseTC), cosmos.ZeroUint()); got != 0 {
		t.Fatalf("expected zero confirmations when confValue is zero, got %d", got)
	}
}

func TestConfirmationRequiredWithConfMultiplier(t *testing.T) {
	t.Parallel()

	coinbaseTC := coinbaseThorchain()
	if coinbaseTC == 0 {
		t.Fatal("coinbase thorchain must be non-zero")
	}

	// 2x multiplier (confMul=20000 bps) doubles confValue, halving confirmations.
	doubledConfValue := cosmos.NewUint(coinbaseTC * 2)
	totalValue := cosmos.NewUint(coinbaseTC * 10) // would normally need 10 confirmations
	if got := confirmationRequired(totalValue, doubledConfValue); got != 5 {
		t.Fatalf("expected 5 confirmations with 2x multiplier, got %d", got)
	}

	// 0.5x multiplier (confMul=5000 bps) halves confValue, doubling confirmations.
	halvedConfValue := cosmos.NewUint(coinbaseTC / 2)
	if got := confirmationRequired(totalValue, halvedConfValue); got != 20 {
		t.Fatalf("expected 20 confirmations with 0.5x multiplier, got %d", got)
	}
}

type confirmationBridge struct {
	thorclient.ThorchainBridge
	asgards          ttypes.Vaults
	confMultiplier   int64
	maxConfirmations int64
}

func (b *confirmationBridge) GetAsgards() (ttypes.Vaults, error) {
	return b.asgards, nil
}

func (b *confirmationBridge) GetMimirWithRef(template, _ string) (int64, error) {
	switch template {
	case constants.MimirTemplateConfMultiplierBasisPoints:
		return b.confMultiplier, nil
	case constants.MimirTemplateMaxConfirmations:
		return b.maxConfirmations, nil
	default:
		return -1, nil
	}
}

func TestGetConfirmationCountAppliesMaxConfirmationCap(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	bridge := &confirmationBridge{
		asgards:          ttypes.Vaults{{PubKey: vaultPubKey, PubKeyEddsa: vaultPubKey, PubKeyFrost: vaultPubKey}},
		confMultiplier:   int64(constants.MaxBasisPts),
		maxConfirmations: 3,
	}
	c := &Client{
		chain:           common.XMRChain,
		cfg:             config.BifrostChainConfiguration{MinConfirmations: 1},
		thorchainBridge: bridge,
		vaultAddressMap: make(map[string]common.Address),
	}

	txIn := stypes.TxIn{
		TxArray: []*stypes.TxInItem{{
			Sender: testOutboundXMRDestination().String(),
			Coins:  common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(coinbaseThorchain()*10))),
		}},
	}

	expected := int64(3)
	if common.CurrentChainNetwork == common.MockNet {
		// Mocknet hard-codes confirmation multiplier and cap to 1 in the shared UTXO helpers.
		expected = 1
	}
	if expected < int64(xmrSpendableConfirmations) {
		expected = int64(xmrSpendableConfirmations)
	}

	if got := c.GetConfirmationCount(txIn); got != expected {
		t.Fatalf("expected max confirmation cap %d, got %d", expected, got)
	}
}

func TestGetConfirmationCountAppliesSpendableFloor(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	bridge := &confirmationBridge{
		asgards:          ttypes.Vaults{{PubKey: vaultPubKey, PubKeyEddsa: vaultPubKey, PubKeyFrost: vaultPubKey}},
		confMultiplier:   int64(constants.MaxBasisPts),
		maxConfirmations: 0,
	}
	c := &Client{
		chain:           common.XMRChain,
		cfg:             config.BifrostChainConfiguration{},
		thorchainBridge: bridge,
		vaultAddressMap: make(map[string]common.Address),
	}

	txIn := stypes.TxIn{
		TxArray: []*stypes.TxInItem{{
			Sender: testOutboundXMRDestination().String(),
			Coins:  common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(coinbaseThorchain()-1))),
		}},
	}

	if got := c.GetConfirmationCount(txIn); got != int64(xmrSpendableConfirmations) {
		t.Fatalf("expected xmr spendability floor %d, got %d", xmrSpendableConfirmations, got)
	}
}

func TestGetConfirmationCountHonorsMinimumFloor(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	bridge := &confirmationBridge{
		asgards:          ttypes.Vaults{{PubKey: vaultPubKey, PubKeyEddsa: vaultPubKey, PubKeyFrost: vaultPubKey}},
		confMultiplier:   int64(constants.MaxBasisPts),
		maxConfirmations: 0,
	}
	c := &Client{
		chain:           common.XMRChain,
		cfg:             config.BifrostChainConfiguration{MinConfirmations: xmrSpendableConfirmations + 2},
		thorchainBridge: bridge,
		vaultAddressMap: make(map[string]common.Address),
	}

	txIn := stypes.TxIn{
		TxArray: []*stypes.TxInItem{{
			Sender: testOutboundXMRDestination().String(),
			Coins:  common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(coinbaseThorchain()-1))),
		}},
	}

	expected := int64(xmrSpendableConfirmations + 2)
	if got := c.GetConfirmationCount(txIn); got != expected {
		t.Fatalf("expected min confirmation floor %d, got %d", expected, got)
	}
}

// --- Outbound observation building ---

func TestBuildOutboundObservationUsesOriginalMemo(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("a", 64)
	outboundMemo := "OUT:" + strings.Repeat("B", 64)

	client := &Client{
		chain: common.XMRChain,
		scanner: &Scanner{
			currentBlockHeight: atomic.NewInt64(123),
			lastFee:            atomic.NewUint64(77),
		},
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddr,
		},
	}

	observation := client.buildOutboundObservation(
		stypes.TxOutItem{
			Chain:            common.XMRChain,
			ToAddress:        common.Address(common.XMRChecksummedDummyAddress('8')),
			Coins:            common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(123))),
			Memo:             "",
			OriginalMemo:     outboundMemo,
			VaultPubKey:      vaultPubKey,
			VaultPubKeyEddsa: vaultPubKey,
			VaultPubKeyFrost: vaultPubKey,
		},
		txHash,
		strings.Repeat("1", 64),
		0,
		nil,
	)
	if observation == nil {
		t.Fatal("expected outbound observation")
	}
	if observation.Memo != outboundMemo {
		t.Fatalf("expected observation memo %q, got %q", outboundMemo, observation.Memo)
	}
	if observation.Sender != vaultAddr.String() {
		t.Fatalf("expected sender %s, got %s", vaultAddr, observation.Sender)
	}
}

func TestBuildOutboundObservationConvertsFallbackNetworkFeeToThorchainUnits(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	client := &Client{
		chain: common.XMRChain,
		scanner: &Scanner{
			currentBlockHeight: atomic.NewInt64(123),
			lastFee:            atomic.NewUint64(120_000 * typicalVaultOutboundWeight),
		},
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddr,
		},
	}

	observation := client.buildOutboundObservation(
		stypes.TxOutItem{
			Chain:            common.XMRChain,
			ToAddress:        common.Address(common.XMRChecksummedDummyAddress('8')),
			Coins:            common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(123))),
			OriginalMemo:     "OUT:" + strings.Repeat("C", 64),
			VaultPubKeyFrost: vaultPubKey,
		},
		strings.Repeat("b", 64),
		strings.Repeat("2", 64),
		0,
		nil,
	)
	if observation == nil {
		t.Fatal("expected outbound observation")
	}
	if len(observation.Gas) != 1 {
		t.Fatalf("expected one gas coin, got %d", len(observation.Gas))
	}
	expectedFallbackFee := uint64(120_000 * typicalVaultOutboundWeight / moneroAtomicToThorchain)
	if observation.Gas[0].Amount.Uint64() != expectedFallbackFee {
		t.Fatalf("expected fallback fee %d thorchain units, got %d", expectedFallbackFee, observation.Gas[0].Amount.Uint64())
	}
}

// TestBuildOutboundObservationAddsUnusedMaxGasForMigration captures the XMR
// migration-specific rule that unused scheduled gas becomes migrated value.
func TestBuildOutboundObservationAddsUnusedMaxGasForMigration(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	client := &Client{
		chain: common.XMRChain,
		scanner: &Scanner{
			currentBlockHeight: atomic.NewInt64(123),
		},
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddr,
		},
	}

	// XMR migration signing can sweep unused MaxGas into the destination vault.
	// The synthetic outbound observation should therefore credit amount plus
	// unused gas, while still reporting the actual fee separately in Gas.
	observation := client.buildOutboundObservation(
		stypes.TxOutItem{
			Chain:            common.XMRChain,
			ToAddress:        common.Address(common.XMRChecksummedDummyAddress('8')),
			Coins:            common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1_000))),
			MaxGas:           common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(50))},
			Memo:             "MIGRATE:123",
			VaultPubKeyFrost: vaultPubKey,
		},
		strings.Repeat("c", 64),
		strings.Repeat("3", 64),
		10*moneroAtomicToThorchain,
		nil,
	)
	if observation == nil {
		t.Fatal("expected outbound observation")
	}
	if got := observation.Coins.GetCoin(common.XMRAsset).Amount.Uint64(); got != 1_040 {
		t.Fatalf("expected migration observation amount 1040 after unused MaxGas sweep, got %d", got)
	}
	if got := observation.Gas.ToCoins().GetCoin(common.XMRAsset).Amount.Uint64(); got != 10 {
		t.Fatalf("expected actual gas 10, got %d", got)
	}
}

func TestBuildOutboundObservationDoesNotSweepUnusedMaxGasForNormalOutbound(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	client := &Client{
		chain: common.XMRChain,
		scanner: &Scanner{
			currentBlockHeight: atomic.NewInt64(123),
		},
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddr,
		},
	}

	observation := client.buildOutboundObservation(
		stypes.TxOutItem{
			Chain:            common.XMRChain,
			ToAddress:        common.Address(common.XMRChecksummedDummyAddress('8')),
			Coins:            common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1_000))),
			MaxGas:           common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(50))},
			Memo:             "OUT:" + strings.Repeat("C", 64),
			VaultPubKeyFrost: vaultPubKey,
		},
		strings.Repeat("d", 64),
		strings.Repeat("4", 64),
		10*moneroAtomicToThorchain,
		nil,
	)
	if observation == nil {
		t.Fatal("expected outbound observation")
	}
	if got := observation.Coins.GetCoin(common.XMRAsset).Amount.Uint64(); got != 1_000 {
		t.Fatalf("expected normal outbound amount to remain 1000, got %d", got)
	}
}
