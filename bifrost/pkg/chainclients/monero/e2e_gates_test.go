package monero

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rs/zerolog/log"
	"go.uber.org/atomic"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type scannerMemoBridge struct {
	thorclient.ThorchainBridge
	memoByTx     map[string]string
	errByTx      map[string]error
	asgards      ttypes.Vaults
	pubkeyVaults ttypes.Vaults
}

func (b *scannerMemoBridge) GetAsgards() (ttypes.Vaults, error) {
	return b.asgards, nil
}

func (b *scannerMemoBridge) GetPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	vaults := append(ttypes.Vaults{}, b.asgards...)
	vaults = append(vaults, b.pubkeyVaults...)

	pairs := make([]thorclient.PubKeyContractAddressPair, 0, len(vaults))
	for _, vault := range vaults {
		if vault.PubKey.IsEmpty() {
			continue
		}
		pairs = append(pairs, thorclient.PubKeyContractAddressPair{
			PubKey:   vault.PubKey,
			Algo:     common.SigningAlgoSecp256k1,
			Inactive: vault.Status == ttypes.VaultStatus_InactiveVault,
		})
	}
	return pairs, nil
}

func (b *scannerMemoBridge) GetVault(pubkey string) (ttypes.Vault, error) {
	for _, vault := range b.asgards {
		if vault.PubKey.String() == pubkey {
			return vault, nil
		}
	}
	for _, vault := range b.pubkeyVaults {
		if vault.PubKey.String() == pubkey {
			return vault, nil
		}
	}
	return ttypes.Vault{}, errors.New("vault not found")
}

func (b *scannerMemoBridge) GetReferenceMemoByTxHash(hash string) (string, error) {
	if err, ok := b.errByTx[hash]; ok {
		return "", err
	}
	return b.memoByTx[hash], nil
}

type solvencyBridge struct {
	thorclient.ThorchainBridge
	// solvencyHalted makes the mock report XMR as solvency-halted on-chain, the
	// only state in which ReportSolvency sends an all-solvent report.
	solvencyHalted bool
	asgards        ttypes.Vaults
}

func (b *solvencyBridge) GetAsgards() (ttypes.Vaults, error) {
	return b.asgards, nil
}

func (b *solvencyBridge) GetMimir(key string) (int64, error) {
	if b.solvencyHalted && key == "SolvencyHaltXMRChain" {
		return 1, nil
	}
	return 0, nil
}

func (b *solvencyBridge) GetBlockHeight() (int64, error) {
	return 100, nil
}

// newFrostTransfersServer creates a mock frost signer server that returns
// transfers, addresses, and chain-query responses for scanner tests.
func newFrostTransfersServer(
	t *testing.T,
	incoming, outgoing []sidecar.Transfer,
	addresses []string,
) *httptest.Server {
	t.Helper()
	chainTxHashes := make([]string, 0, len(incoming)+len(outgoing))
	seen := make(map[string]struct{}, len(incoming)+len(outgoing))
	for _, transfer := range incoming {
		if transfer.TxID == "" {
			continue
		}
		if _, ok := seen[transfer.TxID]; ok {
			continue
		}
		seen[transfer.TxID] = struct{}{}
		chainTxHashes = append(chainTxHashes, transfer.TxID)
	}
	for _, transfer := range outgoing {
		if transfer.TxID == "" {
			continue
		}
		if _, ok := seen[transfer.TxID]; ok {
			continue
		}
		seen[transfer.TxID] = struct{}{}
		chainTxHashes = append(chainTxHashes, transfer.TxID)
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(100))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/scanner/state":
			writeTestScannerState(t, w, 100)
		case strings.HasPrefix(r.URL.Path, "/v1/monero/chain/height"):
			_ = json.NewEncoder(w).Encode(sidecar.ChainHeightResp{Height: 500})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/chain/block/"):
			rawHeight := strings.TrimPrefix(r.URL.Path, "/v1/monero/chain/block/")
			height, err := strconv.ParseInt(rawHeight, 10, 64)
			if err != nil {
				http.Error(w, "invalid block height", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(sidecar.ChainBlockResp{
				Height:       height,
				Reward:       defaultCoinbaseValue + 200000,
				Hash:         testBlockHash(height),
				PreviousHash: testBlockHash(height - 1),
				TxHashes:     chainTxHashes,
			})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/chain/tx/") && strings.HasSuffix(r.URL.Path, "/exists"):
			_ = json.NewEncoder(w).Encode(sidecar.TxExistsResp{Exists: true})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/scanner/sync"):
			_ = json.NewEncoder(w).Encode(sidecar.Transfers{
				Incoming: incoming,
				Outgoing: outgoing,
			})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/transfers"):
			_ = json.NewEncoder(w).Encode(sidecar.Transfers{
				Incoming: incoming,
				Outgoing: outgoing,
			})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/addresses/"):
			_ = json.NewEncoder(w).Encode(sidecar.Addresses{
				Addresses: addresses,
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestXMRScannerInboundNoteMemoAttribution(t *testing.T) {
	t.Parallel()

	txID := strings.Repeat("A", 64)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vaultAddress := vaultAddr.String()
	expectedMemo := "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g"

	frostServer := newFrostTransfersServer(t,
		[]sidecar.Transfer{
			{
				TxID:    txID,
				Address: vaultAddress,
				Amount:  200000,
				Fee:     10000,
				Height:  100,
				Note:    " " + expectedMemo + " ",
			},
		}, nil, []string{vaultAddress})
	defer frostServer.Close()

	var err error
	cfg := config.BifrostChainConfiguration{}
	frostSigner, err := sidecar.NewClientWithEndpoint(cfg, frostServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	bridge := &scannerMemoBridge{
		memoByTx: make(map[string]string),
		errByTx:  make(map[string]error),
		asgards:  ttypes.Vaults{{PubKeyEddsa: vaultPubKey, PubKey: vaultPubKey, PubKeyFrost: vaultPubKey}},
	}
	scanner := NewScanner(log.Logger, frostSigner, bridge, common.XMRChain, common.EmptyPubKey, 72)

	txIn, err := scanner.FetchTxs(100, 100)
	if err != nil {
		t.Fatalf("fetch txs: %v", err)
	}
	if len(txIn.TxArray) != 1 {
		t.Fatalf("expected 1 tx item, got %d", len(txIn.TxArray))
	}
	if txIn.TxArray[0].Memo != expectedMemo {
		t.Fatalf("expected memo %q, got %q", expectedMemo, txIn.TxArray[0].Memo)
	}
	if !txIn.TxArray[0].ObservedVaultPubKey.Equals(vaultPubKey) {
		t.Fatalf("expected observed vault pubkey %s, got %s", vaultPubKey, txIn.TxArray[0].ObservedVaultPubKey)
	}
}

func TestXMRScannerInboundMemolessDeferredAttribution(t *testing.T) {
	t.Parallel()

	txID := strings.Repeat("B", 64)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vaultAddress := vaultAddr.String()

	frostServer := newFrostTransfersServer(t,
		[]sidecar.Transfer{
			{
				TxID:    txID,
				Address: vaultAddress,
				Amount:  200000,
				Fee:     10000,
				Height:  100,
				Note:    "",
			},
		}, nil, []string{vaultAddress})
	defer frostServer.Close()

	var err error
	cfg := config.BifrostChainConfiguration{}
	frostSigner, err := sidecar.NewClientWithEndpoint(cfg, frostServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	bridge := &scannerMemoBridge{
		memoByTx: make(map[string]string),
		errByTx:  map[string]error{txID: errors.New("reference memo not available yet")},
		asgards:  ttypes.Vaults{{PubKeyEddsa: vaultPubKey, PubKey: vaultPubKey, PubKeyFrost: vaultPubKey}},
	}
	scanner := NewScanner(log.Logger, frostSigner, bridge, common.XMRChain, common.EmptyPubKey, 72)

	txIn, err := scanner.FetchTxs(100, 100)
	if err != nil {
		t.Fatalf("fetch txs: %v", err)
	}
	if len(txIn.TxArray) != 1 {
		t.Fatalf("expected 1 tx item, got %d", len(txIn.TxArray))
	}
	if txIn.TxArray[0].Memo != "" {
		t.Fatalf("expected empty memo when reference memo is deferred, got %q", txIn.TxArray[0].Memo)
	}
	senderAddr, addrErr := common.NewAddress(txIn.TxArray[0].Sender)
	if addrErr != nil {
		t.Fatalf("expected synthetic sender marker to be parseable: %v", addrErr)
	}
	if !common.IsXMRUnknownSenderAddress(senderAddr) {
		t.Fatalf("expected unknown sender marker, got %s", txIn.TxArray[0].Sender)
	}
}

func TestXMRScannerOutgoingMemolessDeferredAttribution(t *testing.T) {
	t.Parallel()

	txID := strings.Repeat("C", 64)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vaultAddress := vaultAddr.String()
	destinationAddress := common.XMRChecksummedDummyAddress('8')

	frostServer := newFrostTransfersServer(t,
		nil,
		[]sidecar.Transfer{
			{
				TxID:        txID,
				Address:     destinationAddress,
				FromAddress: vaultAddress,
				VaultPubKey: vaultPubKey.String(),
				Amount:      200000,
				Fee:         10000,
				Height:      100,
				Note:        "",
			},
		}, []string{vaultAddress})
	defer frostServer.Close()

	var err error
	cfg := config.BifrostChainConfiguration{}
	frostSigner, err := sidecar.NewClientWithEndpoint(cfg, frostServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	bridge := &scannerMemoBridge{
		memoByTx: make(map[string]string),
		errByTx:  map[string]error{txID: errors.New("reference memo not available yet")},
		asgards:  ttypes.Vaults{{PubKeyEddsa: vaultPubKey, PubKey: vaultPubKey, PubKeyFrost: vaultPubKey}},
	}
	scanner := NewScanner(log.Logger, frostSigner, bridge, common.XMRChain, common.EmptyPubKey, 72)

	txIn, err := scanner.FetchTxs(100, 100)
	if err != nil {
		t.Fatalf("fetch txs: %v", err)
	}
	if len(txIn.TxArray) != 1 {
		t.Fatalf("expected 1 tx item, got %d", len(txIn.TxArray))
	}
	item := txIn.TxArray[0]
	if item.Tx != txID {
		t.Fatalf("expected tx id %s, got %s", txID, item.Tx)
	}
	if item.Memo != "" {
		t.Fatalf("expected empty memo for deferred outbound reference, got %q", item.Memo)
	}
	if item.Sender != vaultAddress {
		t.Fatalf("expected sender %s, got %s", vaultAddress, item.Sender)
	}
	if item.To != destinationAddress {
		t.Fatalf("expected to %s, got %s", destinationAddress, item.To)
	}
	if !item.ObservedVaultPubKey.Equals(vaultPubKey) {
		t.Fatalf("expected observed vault pubkey %s, got %s", vaultPubKey, item.ObservedVaultPubKey)
	}
}

func TestXMRReportSolvencyMultiVaultAccounting(t *testing.T) {
	t.Parallel()

	pkA, addrA := testVaultPubKeyAndAddress(t)
	pkB, addrB := testVaultPubKeyAndAddress(t)
	addrAString := addrA.String()
	addrBString := addrB.String()
	addrA, err := common.NewAddress(addrAString)
	if err != nil {
		t.Fatalf("new address A: %v", err)
	}
	addrB, err = common.NewAddress(addrBString)
	if err != nil {
		t.Fatalf("new address B: %v", err)
	}

	asgardVaults := ttypes.Vaults{
		{
			PubKeyEddsa: pkA,
			PubKey:      pkA,
			PubKeyFrost: pkA,
			Coins: common.NewCoins(
				common.NewCoin(common.XMRAsset, cosmos.NewUint(100)),
			),
		},
		{
			PubKeyEddsa: pkB,
			PubKey:      pkB,
			PubKeyFrost: pkB,
			Coins: common.NewCoins(
				common.NewCoin(common.XMRAsset, cosmos.NewUint(500)),
			),
		},
	}

	// Mock frost signer that returns balance by vault pub key
	balanceByPK := map[string]sidecar.Balance{
		pkA.String(): {Balance: 100 * moneroAtomicToThorchain, UnlockedBalance: 100 * moneroAtomicToThorchain, ReconciliationState: "reconciled", PendingOutgoingCount: intPtr(0)},
		pkB.String(): {Balance: 10 * moneroAtomicToThorchain, UnlockedBalance: 10 * moneroAtomicToThorchain, ReconciliationState: "reconciled", PendingOutgoingCount: intPtr(0)},
	}
	balanceServer := newFrostBalanceServer(t, balanceByPK)
	defer balanceServer.Close()

	cfg := config.BifrostChainConfiguration{}
	frostSigner, err := sidecar.NewClientWithEndpoint(cfg, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	queue := make(chan stypes.Solvency, 4)
	c := &Client{
		logger:                  log.Logger,
		cfg:                     config.BifrostChainConfiguration{SolvencyBlocks: 10},
		chain:                   common.XMRChain,
		thorchainBridge:         &solvencyBridge{asgards: asgardVaults},
		scanner:                 &Scanner{lastFee: atomic.NewUint64(10)},
		sidecar:                 frostSigner,
		globalSolvencyQueue:     queue,
		vaultAddressMap:         map[string]common.Address{pkA.String(): addrA, pkB.String(): addrB},
		lastSolvencyCheckHeight: atomic.NewInt64(0),
	}

	if err := c.ReportSolvency(10); err != nil {
		t.Fatalf("report solvency: %v", err)
	}
	if len(queue) != 1 {
		t.Fatalf("expected one insolvency message, got %d", len(queue))
	}

	msg := <-queue
	if !msg.PubKey.Equals(pkB) {
		t.Fatalf("expected insolvent vault pubkey %s, got %s", pkB, msg.PubKey)
	}
	xmrCoin := msg.Coins.GetCoin(common.XMRAsset)
	if xmrCoin.Amount.Uint64() != 10 {
		t.Fatalf("expected insolvent vault accounting amount 10 XMR, got %s", xmrCoin.Amount.String())
	}
	if c.lastSolvencyCheckHeight.Load() != 10 {
		t.Fatalf("expected last solvency check height 10, got %d", c.lastSolvencyCheckHeight.Load())
	}
}
