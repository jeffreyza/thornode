package monero

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog/log"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"go.uber.org/atomic"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestFetchTxsAggregatesDuplicateIncomingTxIDs(t *testing.T) {
	t.Parallel()

	dupTxID := strings.Repeat("a", 64)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	frostServer := newFrostTransfersServer(
		t,
		[]sidecar.Transfer{
			{
				TxID:    dupTxID,
				Address: vaultAddr.String(),
				Amount:  uint64(2 * moneroAtomicToThorchain),
				Height:  100,
				Note:    "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
			},
			{
				TxID:    dupTxID,
				Address: vaultAddr.String(),
				Amount:  uint64(3 * moneroAtomicToThorchain),
				Height:  100,
				Note:    "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
			},
		},
		nil,
		[]string{vaultAddr.String()},
	)
	defer frostServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		frostServer.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	scanner := NewScanner(
		log.Logger,
		frostSigner,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{{PubKeyEddsa: vaultPubKey, PubKey: vaultPubKey, PubKeyFrost: vaultPubKey}},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)

	txIn, err := scanner.FetchTxs(100, 100)
	if err != nil {
		t.Fatalf("fetch txs: %v", err)
	}
	if len(txIn.TxArray) != 1 {
		t.Fatalf("expected duplicate txid entries to collapse to one observation, got %d", len(txIn.TxArray))
	}
	amount := txIn.TxArray[0].Coins.GetCoin(common.XMRAsset).Amount
	if !amount.Equal(cosmos.NewUint(5)) {
		t.Fatalf("expected merged amount 5, got %s", amount)
	}
}

func TestFetchTxsSkipsIncomingTransfersWithMismatchedExplicitVaultAttribution(t *testing.T) {
	t.Parallel()

	txID := strings.Repeat("b", 64)
	currentVaultPubKey, currentVaultAddr := testVaultPubKeyAndAddress(t)
	otherVaultPubKey, _ := testVaultPubKeyAndAddress(t)
	frostServer := newFrostTransfersServer(
		t,
		[]sidecar.Transfer{
			{
				TxID:        txID,
				Address:     currentVaultAddr.String(),
				VaultPubKey: currentVaultPubKey.String(),
				Amount:      uint64(2 * moneroAtomicToThorchain),
				Height:      100,
				Note:        "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
			},
			{
				TxID:        txID,
				Address:     currentVaultAddr.String(),
				VaultPubKey: otherVaultPubKey.String(),
				Amount:      uint64(2 * moneroAtomicToThorchain),
				Height:      100,
				Note:        "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
			},
		},
		nil,
		[]string{currentVaultAddr.String()},
	)
	defer frostServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		frostServer.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	scanner := NewScanner(
		log.Logger,
		frostSigner,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{{PubKeyEddsa: currentVaultPubKey, PubKey: currentVaultPubKey, PubKeyFrost: currentVaultPubKey}},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)

	txIn, err := scanner.FetchTxs(100, 100)
	if err != nil {
		t.Fatalf("fetch txs: %v", err)
	}
	if len(txIn.TxArray) != 1 {
		t.Fatalf("expected only the current asgard attribution to survive, got %d items", len(txIn.TxArray))
	}
	amount := txIn.TxArray[0].Coins.GetCoin(common.XMRAsset).Amount
	if !amount.Equal(cosmos.NewUint(2)) {
		t.Fatalf("expected mismatched explicit vault attribution to be skipped, got %s", amount)
	}
	if !txIn.TxArray[0].ObservedVaultPubKey.Equals(currentVaultPubKey) {
		t.Fatalf("expected current asgard vault pubkey %s, got %s", currentVaultPubKey, txIn.TxArray[0].ObservedVaultPubKey)
	}
}

func TestFetchTxsAcceptsCurrentAsgardWithPubKeyFrost(t *testing.T) {
	t.Parallel()

	txID := strings.Repeat("d", 64)
	currentVaultPubKey, currentVaultAddr := testVaultPubKeyAndAddress(t)
	frostServer := newFrostTransfersServer(
		t,
		[]sidecar.Transfer{
			{
				TxID:        txID,
				Address:     currentVaultAddr.String(),
				VaultPubKey: currentVaultPubKey.String(),
				Amount:      uint64(2 * moneroAtomicToThorchain),
				Height:      100,
				Note:        "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
			},
		},
		nil,
		[]string{currentVaultAddr.String()},
	)
	defer frostServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		frostServer.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	scanner := NewScanner(
		log.Logger,
		frostSigner,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{{
				PubKeyEddsa: currentVaultPubKey,
				PubKey:      currentVaultPubKey,
				PubKeyFrost: currentVaultPubKey,
			}},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)

	txIn, err := scanner.FetchTxs(100, 100)
	if err != nil {
		t.Fatalf("fetch txs: %v", err)
	}
	if len(txIn.TxArray) != 1 {
		t.Fatalf("expected current asgard transfer to survive with PubKeyFrost, got %d items", len(txIn.TxArray))
	}
	if !txIn.TxArray[0].ObservedVaultPubKey.Equals(currentVaultPubKey) {
		t.Fatalf("expected current asgard vault pubkey %s, got %s", currentVaultPubKey, txIn.TxArray[0].ObservedVaultPubKey)
	}
}

func TestFetchTxsSkipsCurrentAsgardWithoutPubKeyFrost(t *testing.T) {
	t.Parallel()

	txID := strings.Repeat("f", 64)
	currentVaultPubKey, currentVaultAddr := testVaultPubKeyAndAddress(t)
	frostServer := newFrostTransfersServer(
		t,
		[]sidecar.Transfer{
			{
				TxID:        txID,
				Address:     currentVaultAddr.String(),
				VaultPubKey: currentVaultPubKey.String(),
				Amount:      uint64(2 * moneroAtomicToThorchain),
				Height:      100,
				Note:        "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
			},
		},
		nil,
		[]string{currentVaultAddr.String()},
	)
	defer frostServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		frostServer.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	scanner := NewScanner(
		log.Logger,
		frostSigner,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{{
				PubKeyEddsa: currentVaultPubKey,
				PubKey:      currentVaultPubKey,
			}},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)

	txIn, err := scanner.FetchTxs(100, 100)
	if err != nil {
		t.Fatalf("fetch txs: %v", err)
	}
	if len(txIn.TxArray) != 0 {
		t.Fatalf("expected current asgard transfer to be skipped without PubKeyFrost, got %d items", len(txIn.TxArray))
	}
}

func TestFetchTxsSkipsIncomingChangeForKnownOutgoingSameVault(t *testing.T) {
	t.Parallel()

	txID := strings.Repeat("c", 64)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vaultAddress := vaultAddr.String()
	destinationAddress := common.XMRChecksummedDummyAddress('8')

	frostServer := newFrostTransfersServer(
		t,
		[]sidecar.Transfer{
			{
				TxID:    txID,
				Address: vaultAddress,
				Amount:  uint64(4 * moneroAtomicToThorchain),
				Height:  100,
			},
		},
		[]sidecar.Transfer{
			{
				TxID:        txID,
				Address:     destinationAddress,
				FromAddress: vaultAddress,
				VaultPubKey: vaultPubKey.String(),
				Amount:      uint64(1 * moneroAtomicToThorchain),
				Fee:         uint64(1 * moneroAtomicToThorchain),
				Height:      100,
			},
		},
		[]string{vaultAddress},
	)
	defer frostServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		frostServer.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	scanner := NewScanner(
		log.Logger,
		frostSigner,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{{PubKeyEddsa: vaultPubKey, PubKey: vaultPubKey, PubKeyFrost: vaultPubKey}},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)

	txIn, err := scanner.FetchTxs(100, 100)
	if err != nil {
		t.Fatalf("fetch txs: %v", err)
	}
	if len(txIn.TxArray) != 1 {
		t.Fatalf("expected only the outbound observation, got %d items", len(txIn.TxArray))
	}
	if txIn.TxArray[0].Sender != vaultAddress {
		t.Fatalf("expected outbound sender %s, got %s", vaultAddress, txIn.TxArray[0].Sender)
	}
	if txIn.TxArray[0].To != destinationAddress {
		t.Fatalf("expected outbound destination %s, got %s", destinationAddress, txIn.TxArray[0].To)
	}
	amount := txIn.TxArray[0].Coins.GetCoin(common.XMRAsset).Amount
	if !amount.Equal(cosmos.NewUint(1)) {
		t.Fatalf("expected outbound amount 1, got %s", amount)
	}
}

func TestFetchTxsPreservesInboundAndOutboundViewsForSameTxIDAcrossVaults(t *testing.T) {
	t.Parallel()

	txID := strings.Repeat("d", 64)
	sourceVaultPubKey, sourceVaultAddr := testVaultPubKeyAndAddress(t)
	destVaultPubKey, destVaultAddr := testVaultPubKeyAndAddress(t)

	frostServer := newFrostTransfersServer(
		t,
		[]sidecar.Transfer{
			{
				TxID:    txID,
				Address: destVaultAddr.String(),
				Amount:  uint64(2 * moneroAtomicToThorchain),
				Height:  100,
				Note:    "MIGRATE:100",
			},
		},
		[]sidecar.Transfer{
			{
				TxID:                txID,
				TxKey:               strings.Repeat("1", 64),
				Address:             destVaultAddr.String(),
				FromAddress:         sourceVaultAddr.String(),
				VaultPubKey:         sourceVaultPubKey.String(),
				Amount:              uint64(2 * moneroAtomicToThorchain),
				Fee:                 uint64(1*moneroAtomicToThorchain + 77),
				Height:              100,
				Note:                "MIGRATE:100",
				Origin:              sidecar.OutgoingOriginLocalCombined,
				ObservationEligible: true,
			},
		},
		[]string{sourceVaultAddr.String(), destVaultAddr.String()},
	)
	defer frostServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		frostServer.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	scanner := NewScanner(
		log.Logger,
		frostSigner,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{
				{PubKeyEddsa: sourceVaultPubKey, PubKey: sourceVaultPubKey, PubKeyFrost: sourceVaultPubKey},
				{PubKeyEddsa: destVaultPubKey, PubKey: destVaultPubKey, PubKeyFrost: destVaultPubKey},
			},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)

	txIn, err := scanner.FetchTxs(100, 100)
	if err != nil {
		t.Fatalf("fetch txs: %v", err)
	}
	if len(txIn.TxArray) != 2 {
		t.Fatalf("expected both inbound and outbound observations, got %d items", len(txIn.TxArray))
	}

	var inbound, outbound *stypes.TxInItem
	for _, item := range txIn.TxArray {
		switch {
		case item.ObservedVaultPubKey.Equals(sourceVaultPubKey):
			outbound = item
		case item.ObservedVaultPubKey.Equals(destVaultPubKey):
			inbound = item
		}
	}
	if outbound == nil {
		t.Fatal("expected outbound observation to be preserved")
	}
	if inbound == nil {
		t.Fatal("expected inbound observation to be preserved")
	}
	if !outbound.ObservedVaultPubKey.Equals(sourceVaultPubKey) {
		t.Fatalf("expected outbound vault pubkey %s, got %s", sourceVaultPubKey, outbound.ObservedVaultPubKey)
	}
	if !inbound.ObservedVaultPubKey.Equals(destVaultPubKey) {
		t.Fatalf("expected inbound vault pubkey %s, got %s", destVaultPubKey, inbound.ObservedVaultPubKey)
	}
	if inbound.Sender != sourceVaultAddr.String() {
		t.Fatalf("expected inbound sender to be source vault %s, got %s", sourceVaultAddr, inbound.Sender)
	}
	if inbound.To != destVaultAddr.String() {
		t.Fatalf("expected inbound destination %s, got %s", destVaultAddr, inbound.To)
	}
	if outbound.TxKey != strings.Repeat("1", 64) {
		t.Fatalf("expected outbound tx key to be preserved, got %q", outbound.TxKey)
	}
	if got := inbound.Gas.ToCoins().GetCoin(common.XMRAsset).Amount.Uint64(); got != 1 {
		t.Fatalf("expected inbound to use exact paired outgoing fee at THORChain precision, got %d", got)
	}
	if !inbound.Gas.Equals(outbound.Gas) {
		t.Fatalf("expected paired internal observations to use identical gas: inbound=%s outbound=%s", inbound.Gas, outbound.Gas)
	}
}

func TestFetchTxsSkipsInternalInboundWithoutSourceOutboundMetadata(t *testing.T) {
	t.Parallel()

	txID := strings.Repeat("a1", 32)
	destVaultPubKey, destVaultAddr := testVaultPubKeyAndAddress(t)

	frostServer := newFrostTransfersServer(
		t,
		[]sidecar.Transfer{
			{
				TxID:        txID,
				Address:     destVaultAddr.String(),
				VaultPubKey: destVaultPubKey.String(),
				Amount:      uint64(2 * moneroAtomicToThorchain),
				Height:      100,
				Note:        "MIGRATE:100",
			},
		},
		nil,
		[]string{destVaultAddr.String()},
	)
	defer frostServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		frostServer.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	scanner := NewScanner(
		log.Logger,
		frostSigner,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{
				{PubKeyEddsa: destVaultPubKey, PubKey: destVaultPubKey, PubKeyFrost: destVaultPubKey},
			},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)
	txIn, err := scanner.FetchTxs(100, 100)
	if err != nil {
		t.Fatalf("fetch txs: %v", err)
	}
	if len(txIn.TxArray) != 0 {
		t.Fatalf("expected unknown-source internal inbound to be skipped, got %d items", len(txIn.TxArray))
	}
}

func TestAuthenticatedMigrationReplayObserverAckRejectAndReorg(t *testing.T) {
	t.Parallel()

	sourceVault, sourceAddress := testVaultPubKeyAndAddress(t)
	destinationVault, destinationAddress := testVaultPubKeyAndAddress(t)
	txID := testXMRHash("authenticated-migration-replay")
	blockHash := testXMRHash("authenticated-migration-block")
	parentHash := testXMRHash("authenticated-migration-parent")
	reorgHash := testXMRHash("authenticated-migration-reorg")
	relocatedBlockHash := testXMRHash("authenticated-migration-relocated-block")
	memoText := "MIGRATE:100"
	amountAtomic := uint64(2 * moneroAtomicToThorchain)
	spentRef := testXMROutputRef("authenticated-migration-input", 0, txID)
	proof := sidecar.Transfer{
		TxID:        txID,
		TxKey:       strings.Repeat("1", 64),
		Address:     destinationAddress.String(),
		FromAddress: sourceAddress.String(),
		VaultPubKey: sourceVault.String(),
		Amount:      amountAtomic + 91,
		Fee:         moneroAtomicToThorchain + 77,
		Height:      100,
		Note:        memoText,
		Origin:      sidecar.OutgoingOriginChainCommitted,
		SpentOutputRefs: []sidecar.SpentRef{{
			TxHash:      spentRef.TxHash,
			OutputIndex: spentRef.OutputIndex,
			KeyImage:    spentRef.KeyImage,
		}},
	}

	var canonicalMu sync.RWMutex
	canonicalHash := blockHash
	locationHeight := int64(100)
	locationHash := blockHash
	locationParent := parentHash
	var historicalSyncCalls atomic.Int32
	var markObservedCalls atomic.Int32
	var failMarkObserved atomic.Bool
	failMarkObserved.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			// Registration must carry scanner continuity, otherwise the vault is
			// silently skipped during lookup refresh and later reads as unregistered.
			var req sidecar.RegisterVaultReq
			if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
				http.Error(w, decodeErr.Error(), http.StatusBadRequest)
				return
			}
			nextHeight := uint64(locationHeight) + 1
			_ = json.NewEncoder(w).Encode(sidecar.RegisterVaultResp{
				Registered:         true,
				EffectiveBirthday:  uint64(req.VaultCreationHeight),
				ExpectedNextHeight: &nextHeight,
			})
		case r.Method == http.MethodGet && (r.URL.Path == "/v1/monero/chain/block/100" || r.URL.Path == "/v1/monero/chain/block/104"):
			canonicalMu.RLock()
			height := int64(100)
			hash := canonicalHash
			parent := parentHash
			if r.URL.Path == "/v1/monero/chain/block/104" {
				height = locationHeight
				hash = locationHash
				parent = locationParent
			}
			canonicalMu.RUnlock()
			_ = json.NewEncoder(w).Encode(sidecar.ChainBlockResp{
				Height:       height,
				Hash:         hash,
				PreviousHash: parent,
				TxHashes:     []string{txID},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/chain/tx/"+txID+"/location":
			canonicalMu.RLock()
			resp := sidecar.CanonicalTxLocationResp{
				TxHash:       txID,
				Height:       locationHeight,
				BlockHash:    locationHash,
				PreviousHash: locationParent,
			}
			canonicalMu.RUnlock()
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outputs/mark-observed":
			markObservedCalls.Add(1)
			if failMarkObserved.Load() {
				http.Error(w, "temporary reconciliation failure", http.StatusServiceUnavailable)
				return
			}
			var req sidecar.MarkObservedReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			canonicalMu.RLock()
			height := locationHeight
			hash := locationHash
			servedProof := proof
			servedProof.Height = uint64(locationHeight)
			canonicalMu.RUnlock()
			if req.VaultPubKey != sourceVault.String() || req.TxHash != txID || int64(req.Height) != height || req.BlockHash != hash {
				http.Error(w, "non-canonical mark-observed request", http.StatusConflict)
				return
			}
			_ = json.NewEncoder(w).Encode(servedProof)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/sync":
			historicalSyncCalls.Add(1)
			http.Error(w, "historical scanner rewind forbidden", http.StatusConflict)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := newAuthenticatedInternalInboundReplayStore(db)
	reorgStore := NewReorgStore(db)
	if err = reorgStore.Set(&reorgBlockMeta{Height: 100, BlockHash: blockHash, PreviousHash: parentHash}); err != nil {
		t.Fatal(err)
	}
	scanner := NewScanner(
		log.Logger,
		frostSigner,
		&scannerMemoBridge{asgards: ttypes.Vaults{
			{PubKeyEddsa: sourceVault, PubKey: sourceVault, PubKeyFrost: sourceVault},
			{PubKeyEddsa: destinationVault, PubKey: destinationVault, PubKeyFrost: destinationVault},
		}},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)
	scanner.SetReorgStore(reorgStore)
	scanner.SetAuthenticatedInternalInboundReplayStore(store)
	scanner.setCurrentHeight(100)

	observed := common.NewObservedTx(common.Tx{
		ID:          common.TxID(txID),
		Chain:       common.XMRChain,
		FromAddress: sourceAddress,
		ToAddress:   destinationAddress,
		Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(2))),
		Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		Memo:        memoText,
	}, 100, sourceVault, 100)
	observed.XmrTxKey = strings.Repeat("1", 64)
	observed.SpentOutputRefs = []common.OutputRef{*spentRef}
	queued, err := scanner.enqueueAuthenticatedInternalInboundReplayGuarded(observed, func() (func(), bool) {
		return nil, false
	})
	if !errors.Is(err, errXMRSpentRefsAdmissionRejected) || queued {
		t.Fatalf("rejected replay admission mutated scanner state: queued=%v err=%v", queued, err)
	}
	if count, countErr := store.Count(); countErr != nil || count != 0 {
		t.Fatalf("rejected replay admission persisted work: count=%d err=%v", count, countErr)
	}
	scanner.setCurrentHeight(99)
	queued, err = scanner.EnqueueAuthenticatedInternalInboundReplay(observed)
	if err == nil || queued {
		t.Fatalf("expected replay enrollment to retry while scanner is behind: queued=%v err=%v", queued, err)
	}
	if count, countErr := store.Count(); countErr != nil || count != 0 {
		t.Fatalf("scanner-behind enrollment must not persist an unbound replay: count=%d err=%v", count, countErr)
	}
	scanner.setCurrentHeight(100)
	queued, err = scanner.EnqueueAuthenticatedInternalInboundReplay(observed)
	if err == nil || !queued {
		t.Fatalf("expected replay to be durable before failed reconciliation: queued=%v err=%v", queued, err)
	}
	count, countErr := store.Count()
	if countErr != nil || count != 1 {
		t.Fatalf("failed enrollment lost durable replay: count=%d err=%v", count, countErr)
	}
	failMarkObserved.Store(false)
	queued, err = scanner.EnqueueAuthenticatedInternalInboundReplay(observed)
	if err != nil || !queued {
		t.Fatalf("retry authenticated replay enrollment: queued=%v err=%v", queued, err)
	}

	items, claims, err := scanner.replayAuthenticatedInternalInbounds(101, 101)
	if err != nil || len(items) != 1 || len(claims) != 1 {
		t.Fatalf("first replay: items=%d claims=%d err=%v", len(items), len(claims), err)
	}
	var destinationItem *stypes.TxInItem
	for _, item := range items {
		if item.ObservedVaultPubKey.Equals(destinationVault) {
			destinationItem = item
		}
	}
	if destinationItem == nil || destinationItem.OnObservedTxRejected == nil || destinationItem.OnObservedTxStored == nil {
		t.Fatal("destination replay is missing observer terminal callbacks")
	}
	if destinationItem.TxKey != "" || len(destinationItem.SpentOutputRefs) != 0 ||
		destinationItem.Gas.ToCoins().GetCoin(common.XMRAsset).Amount.Uint64() != 1 {
		t.Fatalf("destination replay leaked outbound metadata or used nondeterministic gas: %+v", destinationItem)
	}
	destinationItem.OnObservedTxRejected(errors.New("observer unavailable"))
	items, claims, err = scanner.replayAuthenticatedInternalInbounds(102, 102)
	if err != nil || len(items) != 1 || len(claims) != 1 {
		t.Fatalf("rejected replay retry: items=%d claims=%d err=%v", len(items), len(claims), err)
	}
	for _, item := range items {
		if item.ObservedVaultPubKey.Equals(destinationVault) {
			item.OnObservedTxStored()
		}
	}
	count, err = store.Count()
	if err != nil || count != 0 {
		t.Fatalf("observer ack did not delete replay: count=%d err=%v", count, err)
	}

	// A transaction re-mined after enrollment but before Observer handoff must
	// atomically rebind the durable replay instead of dropping destination credit.
	if err = store.Enqueue(claims[0].Record); err != nil {
		t.Fatal(err)
	}
	relocatedHeight := uint64(104)
	canonicalMu.Lock()
	canonicalHash = reorgHash
	locationHeight = int64(relocatedHeight)
	locationHash = relocatedBlockHash
	locationParent = reorgHash
	canonicalMu.Unlock()
	marksBeforeFutureReplay := markObservedCalls.Load()
	items, claims, err = scanner.replayAuthenticatedInternalInbounds(103, 103)
	if err != nil || len(items) != 0 || len(claims) != 0 {
		t.Fatalf("future migration must wait for its block: items=%d claims=%d err=%v", len(items), len(claims), err)
	}
	if markObservedCalls.Load() != marksBeforeFutureReplay {
		t.Fatal("future migration crossed MarkObserved before its block was scanned")
	}
	count, err = store.Count()
	if err != nil || count != 1 {
		t.Fatalf("future migration was not retained: count=%d err=%v", count, err)
	}
	futureMeta, futureErr := reorgStore.Get(int64(relocatedHeight))
	if futureErr != nil || futureMeta != nil {
		t.Fatalf("future migration advanced the journal: meta=%+v err=%v", futureMeta, futureErr)
	}
	// Ordinary scanning has now reached the canonical replacement block.
	if err = reorgStore.Set(&reorgBlockMeta{
		Height: int64(relocatedHeight), BlockHash: relocatedBlockHash, PreviousHash: reorgHash,
	}); err != nil {
		t.Fatal(err)
	}
	scanner.setCurrentHeight(int64(relocatedHeight))
	items, claims, err = scanner.replayAuthenticatedInternalInbounds(int64(relocatedHeight), int64(relocatedHeight))
	if err != nil || len(items) != 1 || len(claims) != 1 {
		t.Fatalf("relocated replay must remain deliverable: items=%d claims=%d err=%v", len(items), len(claims), err)
	}
	if items[0].BlockHeight != int64(relocatedHeight) || !items[0].ObservedVaultPubKey.Equals(destinationVault) {
		t.Fatalf("relocated replay retained stale identity: %+v", items[0])
	}
	count, err = store.Count()
	if err != nil || count != 1 {
		t.Fatalf("relocated replay was not retained until observer ack: count=%d err=%v", count, err)
	}
	items[0].OnObservedTxStored()
	count, err = store.Count()
	if err != nil || count != 0 {
		t.Fatalf("relocated replay ack did not clear outbox: count=%d err=%v", count, err)
	}
	if historicalSyncCalls.Load() != 0 {
		t.Fatalf("authenticated replay rewound canonical scanner %d time(s)", historicalSyncCalls.Load())
	}

	// The same transaction may be mined later on the replacement branch after
	// its old replay identity was invalidated. scanner_sync returns the
	// ChainCommitted source as a non-observable authentication proof alongside
	// the destination credit. Pair it for sender/exact fee, but emit only the
	// destination observation.
	// Bound registration birthdays by the height whose transfers are scanned below.
	if err = scanner.refreshAsgardLookup(int64(relocatedHeight)); err != nil {
		t.Fatal(err)
	}
	relocatedProof := proof
	relocatedProof.Height = relocatedHeight
	relocatedProof.ObservationEligible = false
	relocatedIncoming := sidecar.Transfer{
		TxID:        txID,
		Address:     destinationAddress.String(),
		VaultPubKey: destinationVault.String(),
		Amount:      proof.Amount,
		Height:      relocatedHeight,
		Note:        memoText,
	}
	items, err = scanner.transfersToTxInItems(sidecar.Transfers{
		Incoming: []sidecar.Transfer{relocatedIncoming},
		Outgoing: []sidecar.Transfer{relocatedProof},
	}, int64(relocatedHeight))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("relocated canonical scan must emit destination exactly once and never source: %+v", items)
	}
	relocated := items[0]
	if !relocated.ObservedVaultPubKey.Equals(destinationVault) ||
		relocated.Sender != sourceAddress.String() || relocated.To != destinationAddress.String() ||
		relocated.TxKey != "" || len(relocated.SpentOutputRefs) != 0 ||
		relocated.Gas.ToCoins().GetCoin(common.XMRAsset).Amount.Uint64() != 1 {
		t.Fatalf("relocated destination was not paired from authentication-only proof: %+v", relocated)
	}

	incompleteProof := relocatedProof
	incompleteProof.SpentOutputRefs = nil
	items, err = scanner.transfersToTxInItems(sidecar.Transfers{
		Incoming: []sidecar.Transfer{relocatedIncoming},
		Outgoing: []sidecar.Transfer{incompleteProof},
	}, int64(relocatedHeight))
	if err != nil || len(items) != 0 {
		t.Fatalf("incomplete non-observable proof must not attribute or emit an internal transfer: items=%+v err=%v", items, err)
	}

	caseVariantDestination := relocatedProof
	caseVariantDestination.Address = xmrCaseVariant(relocatedProof.Address)
	items, err = scanner.transfersToTxInItems(sidecar.Transfers{
		Incoming: []sidecar.Transfer{relocatedIncoming},
		Outgoing: []sidecar.Transfer{caseVariantDestination},
	}, int64(relocatedHeight))
	if err != nil || len(items) != 0 {
		t.Fatalf("case-variant xmr destination must not authenticate an internal transfer: items=%+v err=%v", items, err)
	}

	caseVariantSource := relocatedProof
	caseVariantSource.FromAddress = xmrCaseVariant(relocatedProof.FromAddress)
	items, err = scanner.transfersToTxInItems(sidecar.Transfers{
		Incoming: []sidecar.Transfer{relocatedIncoming},
		Outgoing: []sidecar.Transfer{caseVariantSource},
	}, int64(relocatedHeight))
	if err != nil || len(items) != 0 {
		t.Fatalf("case-variant xmr source must not authenticate an internal transfer: items=%+v err=%v", items, err)
	}

	// If the replacement block was scanned before the late MarkSpent import,
	// the voter still names the orphaned height. Canonical location recovery
	// must bind the actual replacement height and durably emit only destination.
	if err = reorgStore.Set(&reorgBlockMeta{
		Height:       int64(relocatedHeight),
		BlockHash:    relocatedBlockHash,
		PreviousHash: reorgHash,
	}); err != nil {
		t.Fatal(err)
	}
	scanner.setCurrentHeight(int64(relocatedHeight))
	queued, err = scanner.EnqueueAuthenticatedInternalInboundReplay(observed)
	if err != nil || !queued {
		t.Fatalf("late relocated replay enrollment: queued=%v err=%v", queued, err)
	}
	items, claims, err = scanner.replayAuthenticatedInternalInbounds(int64(relocatedHeight)+1, int64(relocatedHeight)+1)
	if err != nil || len(items) != 1 || len(claims) != 1 {
		t.Fatalf("late relocated replay: items=%d claims=%d err=%v", len(items), len(claims), err)
	}
	if !items[0].ObservedVaultPubKey.Equals(destinationVault) ||
		items[0].BlockHeight != int64(relocatedHeight) || items[0].Sender != sourceAddress.String() ||
		items[0].TxKey != "" || len(items[0].SpentOutputRefs) != 0 {
		t.Fatalf("late relocated replay emitted anything other than the canonical destination: %+v", items[0])
	}
	items[0].OnObservedTxStored()
	items, claims, err = scanner.replayAuthenticatedInternalInbounds(int64(relocatedHeight)+2, int64(relocatedHeight)+2)
	if err != nil || len(items) != 0 || len(claims) != 0 {
		t.Fatalf("acknowledged relocated destination replayed twice: items=%d claims=%d err=%v", len(items), len(claims), err)
	}
}

func xmrCaseVariant(address string) string {
	for i, char := range address {
		switch {
		case char >= 'a' && char <= 'z':
			return address[:i] + strings.ToUpper(string(char)) + address[i+1:]
		case char >= 'A' && char <= 'Z':
			return address[:i] + strings.ToLower(string(char)) + address[i+1:]
		}
	}
	return address
}

func TestFetchTxsSkipsDelayedIncomingChangeForPreviouslyObservedOutgoingSameVault(t *testing.T) {
	t.Parallel()

	txID := strings.Repeat("e", 64)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vaultAddress := vaultAddr.String()
	destinationAddress := common.XMRChecksummedDummyAddress('9')
	scannerNextHeight := atomic.NewUint64(100)

	frostServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/scanner/state":
			writeTestScannerState(t, w, scannerNextHeight.Load())
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/monero/chain/block/"):
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
				TxHashes:     []string{txID},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/sync":
			var req sidecar.SyncReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}

			resp := sidecar.Transfers{}
			switch req.Height {
			case 100:
				resp.Outgoing = []sidecar.Transfer{
					{
						TxID:        txID,
						Address:     destinationAddress,
						FromAddress: vaultAddress,
						VaultPubKey: vaultPubKey.String(),
						Amount:      uint64(1 * moneroAtomicToThorchain),
						Fee:         uint64(1 * moneroAtomicToThorchain),
						Height:      100,
					},
				}
			case 101:
				resp.Incoming = []sidecar.Transfer{
					{
						TxID:        txID,
						Address:     vaultAddress,
						VaultPubKey: vaultPubKey.String(),
						Amount:      uint64(4 * moneroAtomicToThorchain),
						Height:      101,
					},
				}
			}
			scannerNextHeight.Store(uint64(req.Height + 1))
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/monero/addresses/"):
			_ = json.NewEncoder(w).Encode(sidecar.Addresses{Addresses: []string{vaultAddress}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			var req sidecar.RegisterVaultReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(sidecar.RegisterVaultResp{
				Registered:         true,
				EffectiveBirthday:  uint64(req.VaultCreationHeight),
				ExpectedNextHeight: uint64Ptr(scannerNextHeight.Load()),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer frostServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		frostServer.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	scanner := NewScanner(
		log.Logger,
		frostSigner,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{{PubKeyEddsa: vaultPubKey, PubKey: vaultPubKey, PubKeyFrost: vaultPubKey}},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)

	first, err := scanner.FetchTxs(100, 101)
	if err != nil {
		t.Fatalf("fetch txs at height 100: %v", err)
	}
	if len(first.TxArray) != 1 {
		t.Fatalf("expected outbound observation at height 100, got %d items", len(first.TxArray))
	}
	if first.TxArray[0].Sender != vaultAddress {
		t.Fatalf("expected outbound sender %s, got %s", vaultAddress, first.TxArray[0].Sender)
	}

	second, err := scanner.FetchTxs(101, 101)
	if err != nil {
		t.Fatalf("fetch txs at height 101: %v", err)
	}
	if len(second.TxArray) != 0 {
		buf := &bytes.Buffer{}
		_ = json.NewEncoder(buf).Encode(second.TxArray)
		t.Fatalf("expected delayed same-vault incoming change to be skipped, got %d items: %s", len(second.TxArray), buf.String())
	}
}

func TestFetchTxsFailsClosedOnReorgProcessingError(t *testing.T) {
	t.Parallel()

	syncCalls := atomic.NewInt32(0)
	frostServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/chain/block/101":
			_ = json.NewEncoder(w).Encode(sidecar.ChainBlockResp{
				Height:       101,
				Reward:       defaultCoinbaseValue,
				Hash:         testBlockHash(101),
				PreviousHash: testBlockHash(100),
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/chain/block/100":
			http.Error(w, "reorg confirmation failed", http.StatusInternalServerError)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/sync":
			syncCalls.Inc()
			_ = json.NewEncoder(w).Encode(sidecar.Transfers{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer frostServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		frostServer.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	memStorage := storage.NewMemStorage()
	db, err := leveldb.Open(memStorage, nil)
	if err != nil {
		t.Fatalf("open in-memory leveldb: %v", err)
	}
	defer db.Close()

	reorgStore := NewReorgStore(db)
	if setErr := reorgStore.Set(&reorgBlockMeta{
		Height:               100,
		BlockHash:            testStaleBlockHash(100),
		PreviousHash:         testBlockHash(99),
		CustomerTransactions: []string{"tx-drop"},
	}); setErr != nil {
		t.Fatalf("set reorg meta: %v", setErr)
	}

	scanner := NewScanner(
		log.Logger,
		frostSigner,
		&scannerMemoBridge{asgards: ttypes.Vaults{}},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)
	scanner.SetReorgStore(reorgStore)

	_, err = scanner.FetchTxs(101, 101)
	if err == nil {
		t.Fatal("expected reorg processing error to abort block processing")
	}
	if syncCalls.Load() != 0 {
		t.Fatalf("expected scanner sync to be skipped after reorg failure, got %d calls", syncCalls.Load())
	}
}
