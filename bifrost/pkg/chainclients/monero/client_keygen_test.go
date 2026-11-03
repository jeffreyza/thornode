package monero

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blang/semver"
	"github.com/rs/zerolog/log"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	tsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

type mockFrostKeygenTSS struct {
	tssp.Server

	calls   int
	lastReq tssp.FrostKeygenRequest
	sidecar tssp.FrostSidecarKeygenClient

	resp tssp.FrostKeygenResponse
	err  error
}

type unavailableKeygenVersionBridge struct {
	thorclient.ThorchainBridge
	calls int
}

func (b *unavailableKeygenVersionBridge) GetThorchainVersion() (semver.Version, error) {
	b.calls++
	return semver.Version{}, errors.New("THORChain version unavailable")
}

// Neither an unavailable height endpoint nor the legacy TSS version selector
// should prevent XMR from joining. Real DKG errors still fail the attempt.
func TestClientKeygenSkipsPreJoinQueries(t *testing.T) {
	var queries int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries++
		http.Error(w, "sidecar unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &unavailableKeygenVersionBridge{}
	dkgErr := errors.New("DKG round 1 unavailable")
	tssServer := &mockFrostKeygenTSS{err: dkgErr}
	client := &Client{
		chain:           common.XMRChain,
		sidecar:         frostSigner,
		scanner:         NewScanner(log.Logger, frostSigner, nil, common.XMRChain, common.EmptyPubKey, 72),
		tssServer:       tssServer,
		thorchainBridge: bridge,
	}
	_, err = client.Keygen(chainclienttypes.CustomKeygenRequest{Members: []string{"member-a"}, BlockHeight: 101})
	if !errors.Is(err, dkgErr) || tssServer.calls != 1 {
		t.Fatalf("expected DKG failure after joining, got calls=%d err=%v", tssServer.calls, err)
	}
	if queries != 0 || bridge.calls != 0 {
		t.Fatalf("pre-join queries: sidecar=%d THORChain=%d, want zero", queries, bridge.calls)
	}
}

func (m *mockFrostKeygenTSS) FrostKeygen(req tssp.FrostKeygenRequest, sidecar tssp.FrostSidecarKeygenClient) (tssp.FrostKeygenResponse, error) {
	m.calls++
	m.lastReq = req
	m.sidecar = sidecar
	return m.resp, m.err
}

func newTestSidecarClient(t *testing.T) *sidecar.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/monero/vaults/register":
			var req sidecar.RegisterVaultReq
			if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
				http.Error(w, decodeErr.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(sidecar.RegisterVaultResp{
				Registered:         true,
				EffectiveBirthday:  uint64(req.VaultCreationHeight),
				ExpectedNextHeight: uint64Ptr(0),
			})
		case "/v1/monero/chain/height":
			_ = json.NewEncoder(w).Encode(sidecar.ChainHeightResp{Height: 100})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	signer, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	return signer
}

func newTestVaultAddressStore(t *testing.T) *VaultAddressStore {
	t.Helper()
	mem := storage.NewMemStorage()
	db, err := leveldb.Open(mem, nil)
	if err != nil {
		t.Fatalf("open leveldb: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return NewVaultAddressStore(db)
}

func TestClientKeygenPersistsVaultAddress(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	frostVaultPubKey, expectedAddress := testVaultPubKeyAndAddress(t)
	groupKey, err := groupKeyHexFromVaultPubKey(frostVaultPubKey)
	if err != nil {
		t.Fatalf("group key: %v", err)
	}
	tssServer := &mockFrostKeygenTSS{
		resp: tssp.FrostKeygenResponse{
			Address:     expectedAddress.String(),
			GroupKey:    groupKey,
			KeyBytesHex: "cafebabe",
			Status:      tsscommon.Success,
		},
	}
	store := newTestVaultAddressStore(t)

	frostSigner := newTestSidecarClient(t)
	c := &Client{
		chain:             common.XMRChain,
		sidecar:           frostSigner,
		tssServer:         tssServer,
		scanner:           NewScanner(log.Logger, frostSigner, nil, common.XMRChain, common.EmptyPubKey, 72),
		vaultAddressMap:   make(map[string]common.Address),
		vaultAddressStore: store,
	}

	result, err := c.Keygen(chainclienttypes.CustomKeygenRequest{
		Members:         []string{" member-a ", "member-b"},
		BlockHeight:     101,
		PoolPubKeyEddsa: vaultPubKey,
	})
	if err != nil {
		t.Fatalf("Keygen failed: %v", err)
	}
	if len(result.Blame) != 0 {
		t.Fatalf("expected no blame, got %+v", result.Blame)
	}
	if !result.Address.Equals(expectedAddress) {
		t.Fatalf("expected address %s, got %s", expectedAddress, result.Address)
	}
	if !result.PoolPubKeyEddsa.IsEmpty() {
		t.Fatalf("expected no eddsa pool pubkey, got %s", result.PoolPubKeyEddsa)
	}
	if !result.PoolPubKeyFrost.Equals(frostVaultPubKey) {
		t.Fatalf("expected frost pool pubkey %s, got %s", frostVaultPubKey, result.PoolPubKeyFrost)
	}
	if result.FrostKeyshareHex != "cafebabe" {
		t.Fatalf("expected frost keyshare hex cafebabe, got %s", result.FrostKeyshareHex)
	}

	if tssServer.calls != 1 {
		t.Fatalf("expected one FrostKeygen call, got %d", tssServer.calls)
	}
	if tssServer.sidecar == nil {
		t.Fatalf("expected non-nil sidecar adapter")
	}
	if tssServer.lastReq.VaultPubKey == "" {
		t.Fatal("expected deterministic non-empty vault key label")
	}
	if tssServer.lastReq.VaultPubKey == frostVaultPubKey.String() {
		t.Fatalf("expected synthetic monovault key label, got final vault pubkey %s", tssServer.lastReq.VaultPubKey)
	}
	if tssServer.lastReq.ExpectedAddr != "" {
		t.Fatalf("expected empty expected_addr, got %s", tssServer.lastReq.ExpectedAddr)
	}
	if tssServer.lastReq.BlockHeight != 101 {
		t.Fatalf("expected block height 101, got %d", tssServer.lastReq.BlockHeight)
	}
	if len(tssServer.lastReq.SignerPubKeys) != 2 || tssServer.lastReq.SignerPubKeys[0] != "member-a" || tssServer.lastReq.SignerPubKeys[1] != "member-b" {
		t.Fatalf("unexpected signer set: %+v", tssServer.lastReq.SignerPubKeys)
	}

	mapped, ok := c.cachedVaultAddress(frostVaultPubKey)
	if !ok {
		t.Fatalf("expected in-memory vault mapping for %s", frostVaultPubKey)
	}
	if !mapped.Equals(expectedAddress) {
		t.Fatalf("expected mapped address %s, got %s", expectedAddress, mapped)
	}

	persisted, err := store.Get(frostVaultPubKey.String())
	if err != nil {
		t.Fatalf("expected persisted vault mapping: %v", err)
	}
	if persisted != expectedAddress.String() {
		t.Fatalf("expected persisted address %s, got %s", expectedAddress, persisted)
	}
}

func TestClientKeygenFailsWhenAddressMismatchesDerived(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	frostVaultPubKey, _ := testVaultPubKeyAndAddress(t)
	_, wrongAddress := testVaultPubKeyAndAddress(t)
	groupKey, err := groupKeyHexFromVaultPubKey(frostVaultPubKey)
	if err != nil {
		t.Fatalf("group key: %v", err)
	}
	tssServer := &mockFrostKeygenTSS{
		resp: tssp.FrostKeygenResponse{
			Address:  wrongAddress.String(),
			GroupKey: groupKey,
			Status:   tsscommon.Success,
		},
	}
	store := newTestVaultAddressStore(t)

	frostSigner := newTestSidecarClient(t)
	c := &Client{
		chain:             common.XMRChain,
		sidecar:           frostSigner,
		tssServer:         tssServer,
		scanner:           NewScanner(log.Logger, frostSigner, nil, common.XMRChain, common.EmptyPubKey, 72),
		vaultAddressMap:   make(map[string]common.Address),
		vaultAddressStore: store,
	}

	_, keygenErr := c.Keygen(chainclienttypes.CustomKeygenRequest{
		Members:         []string{"member-a", "member-b"},
		BlockHeight:     101,
		PoolPubKeyEddsa: vaultPubKey,
	})
	if keygenErr == nil {
		t.Fatalf("expected keygen mismatch error")
	}
	if !strings.Contains(keygenErr.Error(), "returned address") {
		t.Fatalf("unexpected error: %v", keygenErr)
	}

	if _, ok := c.cachedVaultAddress(frostVaultPubKey); ok {
		t.Fatalf("unexpected vault mapping persisted on mismatched result")
	}
	if _, getErr := store.Get(frostVaultPubKey.String()); getErr == nil {
		t.Fatalf("expected no persisted mapping on mismatched result")
	}
}

func TestClientKeygenRegistersObservedMoneroHeightAsBirthday(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	frostVaultPubKey, expectedAddress := testVaultPubKeyAndAddress(t)
	groupKey, err := groupKeyHexFromVaultPubKey(frostVaultPubKey)
	if err != nil {
		t.Fatalf("group key: %v", err)
	}
	tssServer := &mockFrostKeygenTSS{
		resp: tssp.FrostKeygenResponse{
			Address:              expectedAddress.String(),
			GroupKey:             groupKey,
			KeyBytesHex:          "beadfeed",
			ObservedMoneroHeight: 777,
			Status:               tsscommon.Success,
		},
	}
	store := newTestVaultAddressStore(t)
	registerReqCh := make(chan sidecar.RegisterVaultReq, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/monero/vaults/register":
			var req sidecar.RegisterVaultReq
			if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
				t.Fatalf("decode register request: %v", decodeErr)
			}
			registerReqCh <- req
			_ = json.NewEncoder(w).Encode(sidecar.RegisterVaultResp{
				Registered:         true,
				EffectiveBirthday:  777,
				ExpectedNextHeight: uint64Ptr(777),
			})
		case "/v1/monero/chain/height":
			_ = json.NewEncoder(w).Encode(sidecar.ChainHeightResp{Height: 100})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:             common.XMRChain,
		sidecar:           frostSigner,
		tssServer:         tssServer,
		scanner:           NewScanner(log.Logger, frostSigner, nil, common.XMRChain, common.EmptyPubKey, 72),
		vaultAddressMap:   make(map[string]common.Address),
		vaultAddressStore: store,
	}

	if _, err := c.Keygen(chainclienttypes.CustomKeygenRequest{
		Members:         []string{"member-a", "member-b"},
		BlockHeight:     101,
		PoolPubKeyEddsa: vaultPubKey,
	}); err != nil {
		t.Fatalf("Keygen failed: %v", err)
	}

	select {
	case req := <-registerReqCh:
		if req.VaultCreationHeight != 777 {
			t.Fatalf("expected birthday 777, got %d", req.VaultCreationHeight)
		}
	default:
		t.Fatal("expected register request")
	}
}

func TestClientKeygenAlignsFirstVaultRegistrationWithLiveBlockScanner(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	frostVaultPubKey, expectedAddress := testVaultPubKeyAndAddress(t)
	groupKey, err := groupKeyHexFromVaultPubKey(frostVaultPubKey)
	if err != nil {
		t.Fatalf("group key: %v", err)
	}
	tssServer := &mockFrostKeygenTSS{
		resp: tssp.FrostKeygenResponse{
			Address:              expectedAddress.String(),
			GroupKey:             groupKey,
			KeyBytesHex:          "beadfeed",
			ObservedMoneroHeight: 777,
			Status:               tsscommon.Success,
		},
	}
	store := newTestVaultAddressStore(t)

	registerReqCh := make(chan sidecar.RegisterVaultReq, 1)
	signerNextHeight := int64(778)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/monero/vaults/register":
			var req sidecar.RegisterVaultReq
			if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
				http.Error(w, decodeErr.Error(), http.StatusBadRequest)
				return
			}
			// Model the production signer behavior: the first vault birthday
			// moves its scanner cursor back to the earliest required height.
			signerNextHeight = req.VaultCreationHeight
			registerReqCh <- req
			_ = json.NewEncoder(w).Encode(sidecar.RegisterVaultResp{
				Registered:         true,
				EffectiveBirthday:  uint64(req.VaultCreationHeight),
				ExpectedNextHeight: uint64Ptr(uint64(signerNextHeight)),
			})
		case "/v1/monero/chain/height":
			_ = json.NewEncoder(w).Encode(sidecar.ChainHeightResp{Height: 1_000})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	scanner := NewScanner(log.Logger, frostSigner, nil, common.XMRChain, common.EmptyPubKey, 72)
	chainScanner, err := blockscanner.NewBlockScanner(
		config.BifrostBlockScannerConfiguration{
			StartBlockHeight: 511,
			ChainID:          common.XMRChain,
		},
		blockscanner.NewMockScannerStorage(),
		failClosedTestMetrics(t),
		failClosedBridge{},
		scanner,
	)
	if err != nil {
		t.Fatalf("new block scanner: %v", err)
	}

	c := &Client{
		chain:             common.XMRChain,
		sidecar:           frostSigner,
		tssServer:         tssServer,
		scanner:           scanner,
		blockScanner:      chainScanner,
		vaultAddressMap:   make(map[string]common.Address),
		vaultAddressStore: store,
	}

	if _, err = c.Keygen(chainclienttypes.CustomKeygenRequest{
		Members:         []string{"member-a", "member-b"},
		BlockHeight:     101,
		PoolPubKeyEddsa: vaultPubKey,
	}); err != nil {
		t.Fatalf("Keygen failed: %v", err)
	}

	select {
	case req := <-registerReqCh:
		if req.VaultCreationHeight != 512 {
			t.Fatalf("expected first vault birthday at Bifrost next height 512, got %d", req.VaultCreationHeight)
		}
	case <-time.After(time.Second):
		t.Fatal("expected register request")
	}
	if signerNextHeight != 512 {
		t.Fatalf("expected signer scanner rewound to 512, got %d", signerNextHeight)
	}
	if got := chainScanner.PreviousHeight(); got != 511 {
		t.Fatalf("keygen must not advance Bifrost scanner cursor: expected 511, got %d", got)
	}
}

func TestXMRVaultRegistrationBirthdayAlignsFirstVaultWithBlockScanner(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name           string
		observedHeight int64
		nextScanHeight int64
		want           int64
	}{
		{
			name:           "mocknet first vault",
			observedHeight: 567,
			nextScanHeight: 512,
			want:           512,
		},
		{
			name:           "scanner already at birthday",
			observedHeight: 567,
			nextScanHeight: 567,
			want:           567,
		},
		{
			name:           "scanner already ahead",
			observedHeight: 567,
			nextScanHeight: 601,
			want:           567,
		},
		{
			name:           "key only registration",
			observedHeight: 0,
			nextScanHeight: 512,
			want:           0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := xmrVaultRegistrationBirthday(test.observedHeight, test.nextScanHeight); got != test.want {
				t.Fatalf("expected registration birthday %d, got %d", test.want, got)
			}
		})
	}
}

func TestClientKeygenAllowsMonovaultRequestWithoutPreseededEddsaKey(t *testing.T) {
	t.Parallel()

	frostVaultPubKey, expectedAddress := testVaultPubKeyAndAddress(t)
	groupKey, err := groupKeyHexFromVaultPubKey(frostVaultPubKey)
	if err != nil {
		t.Fatalf("group key: %v", err)
	}
	tssServer := &mockFrostKeygenTSS{
		resp: tssp.FrostKeygenResponse{
			Address:     expectedAddress.String(),
			GroupKey:    groupKey,
			KeyBytesHex: "c001d00d",
			Status:      tsscommon.Success,
		},
	}
	store := newTestVaultAddressStore(t)

	frostSigner := newTestSidecarClient(t)
	c := &Client{
		chain:             common.XMRChain,
		sidecar:           frostSigner,
		tssServer:         tssServer,
		scanner:           NewScanner(log.Logger, frostSigner, nil, common.XMRChain, common.EmptyPubKey, 72),
		vaultAddressMap:   make(map[string]common.Address),
		vaultAddressStore: store,
	}

	result, err := c.Keygen(chainclienttypes.CustomKeygenRequest{
		Members:     []string{"member-b", "member-a"},
		BlockHeight: 202,
	})
	if err != nil {
		t.Fatalf("Keygen failed: %v", err)
	}
	if !result.PoolPubKeyEddsa.IsEmpty() {
		t.Fatalf("expected no eddsa pool pubkey, got %s", result.PoolPubKeyEddsa)
	}
	if tssServer.lastReq.VaultPubKey == "" {
		t.Fatalf("expected deterministic non-empty vault key label")
	}
	if tssServer.lastReq.VaultPubKey == frostVaultPubKey.String() {
		t.Fatalf("expected synthetic monovault key label, got final vault pubkey")
	}
	if !strings.HasPrefix(tssServer.lastReq.VaultPubKey, "tthorpub1") {
		t.Fatalf("unexpected monovault key label: %s", tssServer.lastReq.VaultPubKey)
	}
}
