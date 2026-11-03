package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestNonSignerCanObserveImportedOutbound verifies that a node which did not
// participate in FROST signing can still observe the outbound after a
// peer-signed attestation imports the sidecar-verified outbound metadata.
func TestNonSignerCanObserveImportedOutbound(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	outboundTxID := strings.Repeat("ab", 32)
	outboundTxKey := strings.Repeat("12", 32)
	destAddr := testOutboundXMRDestination()
	outboundAmount := uint64(5_000_000_000_000) // 0.5 XMR in piconero
	outboundFee := uint64(30_000_000)           // typical fee

	outboundTransfer := sidecar.Transfer{
		TxID:          outboundTxID,
		TxKey:         outboundTxKey,
		Address:       destAddr.String(),
		FromAddress:   vaultAddr.String(),
		VaultPubKey:   vaultPubKey.String(),
		Amount:        outboundAmount,
		Fee:           outboundFee,
		Height:        100,
		Confirmations: 5,
		Note:          "OUT:" + strings.Repeat("cc", 32),
		Locked:        false,
	}

	// signerFrost returns the outgoing transfer (signing node has the record).
	signerFrost := newMockFrostServer(t, 100, []sidecar.Transfer{outboundTransfer})
	defer signerFrost.Close()

	// nonSignerFrost starts empty; MarkOutputsSpent imports the verified
	// outbound record before the scanner is expected to attest locally.
	nonSignerFrost := newMockFrostServer(t, 100, nil)
	defer nonSignerFrost.Close()

	// --- Signer node: should observe the outbound ---
	signerScanner := newTestScanner(t, signerFrost.URL, vaultPubKey)
	signerTxIn, err := signerScanner.FetchTxs(100, 150)
	if err != nil {
		t.Fatalf("signer FetchTxs: %v", err)
	}

	var signerOutbound int
	for _, item := range signerTxIn.TxArray {
		if item.Sender == vaultAddr.String() {
			if item.TxKey != outboundTxKey {
				t.Fatalf("signer outbound missing tx key: got %q", item.TxKey)
			}
			signerOutbound++
		}
	}
	if signerOutbound == 0 {
		t.Fatal("signer node should observe the outbound transfer but found none")
	}

	// --- Non-signer node: should observe after sidecar import ---
	nonSignerScanner := newTestScanner(t, nonSignerFrost.URL, vaultPubKey)
	importOutboundRecord(t, nonSignerScanner.sidecar, outboundTransfer)
	nonSignerTxIn, err := nonSignerScanner.FetchTxs(100, 150)
	if err != nil {
		t.Fatalf("non-signer FetchTxs: %v", err)
	}

	var nonSignerOutbound int
	for _, item := range nonSignerTxIn.TxArray {
		if item.Sender == vaultAddr.String() {
			if item.TxKey != outboundTxKey {
				t.Fatalf("non-signer outbound missing imported tx key: got %q", item.TxKey)
			}
			nonSignerOutbound++
		}
	}

	if nonSignerOutbound != signerOutbound {
		t.Fatalf("expected non-signer to match signer outbounds, signer=%d non-signer=%d", signerOutbound, nonSignerOutbound)
	}
}

// TestTwoOfThreeSignerConsensus verifies that in a 2-of-3 setup both signers
// produce identical observations and the non-signer catches up after importing
// the sidecar-verified outbound metadata.
func TestTwoOfThreeSignerConsensus(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	outboundTxID := strings.Repeat("de", 32)
	outboundTxKey := strings.Repeat("34", 32)
	destAddr := testOutboundXMRDestination()
	memo := "OUT:" + strings.Repeat("ff", 32)

	transfer := sidecar.Transfer{
		TxID:          outboundTxID,
		TxKey:         outboundTxKey,
		Address:       destAddr.String(),
		FromAddress:   vaultAddr.String(),
		VaultPubKey:   vaultPubKey.String(),
		Amount:        10_000_000_000_000,
		Fee:           25_000_000,
		Height:        200,
		Confirmations: 3,
		Note:          memo,
		Locked:        false,
	}

	signer1Frost := newMockFrostServer(t, 200, []sidecar.Transfer{transfer})
	defer signer1Frost.Close()
	signer2Frost := newMockFrostServer(t, 200, []sidecar.Transfer{transfer})
	defer signer2Frost.Close()
	nonSignerFrost := newMockFrostServer(t, 200, nil)
	defer nonSignerFrost.Close()

	s1 := newTestScanner(t, signer1Frost.URL, vaultPubKey)
	s2 := newTestScanner(t, signer2Frost.URL, vaultPubKey)
	ns := newTestScanner(t, nonSignerFrost.URL, vaultPubKey)

	txIn1, err := s1.FetchTxs(200, 250)
	if err != nil {
		t.Fatalf("signer1 FetchTxs: %v", err)
	}
	txIn2, err := s2.FetchTxs(200, 250)
	if err != nil {
		t.Fatalf("signer2 FetchTxs: %v", err)
	}
	importOutboundRecord(t, ns.sidecar, transfer)
	txInNS, err := ns.FetchTxs(200, 250)
	if err != nil {
		t.Fatalf("non-signer FetchTxs: %v", err)
	}

	outbound1 := countOutboundsFrom(txIn1.TxArray, vaultAddr.String())
	outbound2 := countOutboundsFrom(txIn2.TxArray, vaultAddr.String())
	outboundNS := countOutboundsFrom(txInNS.TxArray, vaultAddr.String())

	if outbound1 != outbound2 {
		t.Fatalf("signers disagree: signer1=%d signer2=%d", outbound1, outbound2)
	}
	if outbound1 == 0 {
		t.Fatal("both signers should observe the outbound")
	}

	if outboundNS != outbound1 {
		t.Fatalf("non-signer should match signers after import, non-signer=%d signers=%d", outboundNS, outbound1)
	}
}

// TestNonSignerMissingFromAddressFailsClosed verifies that resolveOutgoingSender
// rejects outgoing transfers when from_address is empty (as returned to non-signers).
func TestNonSignerMissingFromAddressFailsClosed(t *testing.T) {
	t.Parallel()

	_, vaultAddr := testVaultPubKeyAndAddress(t)
	s := &Scanner{
		logger: log.Logger,
	}

	// Non-signer gets a transfer with empty from_address and vault_pub_key.
	sender, pk := s.resolveOutgoingSender(sidecar.Transfer{
		TxID:        strings.Repeat("ab", 32),
		Address:     vaultAddr.String(),
		FromAddress: "",
		VaultPubKey: "",
		Amount:      1_000_000_000_000,
	})
	if sender != "" || !pk.IsEmpty() {
		t.Fatalf("expected fail-closed for missing metadata, got sender=%q pk=%s", sender, pk)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func countOutboundsFrom(items []*stypes.TxInItem, sender string) int {
	count := 0
	for _, item := range items {
		if item.Sender == sender {
			count++
		}
	}
	return count
}

func importOutboundRecord(t *testing.T, signer *sidecar.Client, transfer sidecar.Transfer) {
	t.Helper()
	result, err := signer.MarkSpent(
		transfer.VaultPubKey,
		transfer.TxID,
		[]sidecar.SpentRef{{
			TxHash:      strings.Repeat("11", 32),
			OutputIndex: 0,
			KeyImage:    strings.Repeat("22", 32),
			SpendTxHash: transfer.TxID,
		}},
		false,
		&sidecar.OutboundMeta{
			FromAddress: transfer.FromAddress,
			ToAddress:   transfer.Address,
			TxKey:       transfer.TxKey,
			Amount:      transfer.Amount,
			Fee:         transfer.Fee,
			Memo:        transfer.Note,
		},
	)
	if err != nil {
		t.Fatalf("import outbound record: %v", err)
	}
	if result.Marked == 0 {
		t.Fatal("expected mark-spent import to mark at least one output")
	}
}

func newTestScanner(t *testing.T, frostURL string, vaultPubKey common.PubKey) *Scanner {
	t.Helper()
	frostSigner, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		frostURL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	bridge := &scannerMemoBridge{asgards: ttypes.Vaults{}}
	return NewScanner(log.Logger, frostSigner, bridge, common.XMRChain, common.EmptyPubKey, 72)
}

func newMockFrostServer(t *testing.T, blockHeight int64, outgoing []sidecar.Transfer) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	currentOutgoing := append([]sidecar.Transfer(nil), outgoing...)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/scanner/state":
			writeTestScannerState(t, w, uint64(blockHeight))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/chain/height":
			_ = json.NewEncoder(w).Encode(sidecar.ChainHeightResp{Height: blockHeight + 50})

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/monero/chain/block/"):
			rawHeight := strings.TrimPrefix(r.URL.Path, "/v1/monero/chain/block/")
			height, err := strconv.ParseInt(rawHeight, 10, 64)
			if err != nil {
				http.Error(w, "invalid height", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(sidecar.ChainBlockResp{
				Height:       height,
				Reward:       defaultCoinbaseValue + 200000,
				Hash:         testBlockHash(height),
				PreviousHash: testBlockHash(height - 1),
				TxHashes:     []string{},
			})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/sync":
			mu.Lock()
			respOutgoing := append([]sidecar.Transfer(nil), currentOutgoing...)
			mu.Unlock()
			resp := sidecar.Transfers{}
			if respOutgoing != nil {
				resp.Outgoing = respOutgoing
			}
			_ = json.NewEncoder(w).Encode(resp)

		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outputs/mark-spent":
			var req sidecar.MarkSpentReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid mark-spent request", http.StatusBadRequest)
				return
			}
			if req.TxHash != "" && req.FromAddress != "" && req.ToAddress != "" && req.VaultPubKey != "" {
				mu.Lock()
				currentOutgoing = append(currentOutgoing, sidecar.Transfer{
					TxID:        req.TxHash,
					TxKey:       req.TxKey,
					Address:     req.ToAddress,
					FromAddress: req.FromAddress,
					VaultPubKey: req.VaultPubKey,
					Amount:      req.Amount,
					Fee:         req.Fee,
					Height:      uint64(blockHeight),
					Note:        req.Memo,
				})
				mu.Unlock()
			}
			_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})

		default:
			http.NotFound(w, r)
		}
	}))
}
