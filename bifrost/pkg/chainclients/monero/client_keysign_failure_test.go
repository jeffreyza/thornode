package monero

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blang/semver"
	uberatomic "go.uber.org/atomic"

	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	bifrosttss "gitlab.com/thorchain/thornode/v3/bifrost/tss"
	gotssblame "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	gotsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
)

type keysignFailureBridge struct {
	thorclient.ThorchainBridge

	calls      int
	lastBlame  ttypes.Blame
	lastHeight int64
	lastMemo   string
	lastCoins  common.Coins
	lastPubKey common.PubKey
	retErr     error
	vaults     map[string]ttypes.Vault

	keysignPolicies map[int64]thorclient.KeysignPolicy
}

func (b *keysignFailureBridge) GetVault(pubkey string) (ttypes.Vault, error) {
	if b.vaults == nil {
		return ttypes.Vault{}, errors.New("vault lookup is unavailable in test bridge")
	}
	vault, ok := b.vaults[pubkey]
	if !ok {
		return ttypes.Vault{}, errors.New("vault not found")
	}
	return vault, nil
}

func (b *keysignFailureBridge) GetAsgards() (ttypes.Vaults, error) {
	if b.vaults == nil {
		return nil, errors.New("asgards lookup is unavailable in test bridge")
	}
	out := make(ttypes.Vaults, 0, len(b.vaults))
	for _, vault := range b.vaults {
		out = append(out, vault)
	}
	return out, nil
}

// GetKeysignParty exposes vault membership to keysign-failure tests through
// the legacy bridge method.
func (b *keysignFailureBridge) GetKeysignParty(vaultPubKey common.PubKey, _ int64) (common.PubKeys, error) {
	vault, err := b.GetVault(vaultPubKey.String())
	if err != nil {
		return nil, err
	}
	members := vault.GetMembership()
	if len(members) > 0 {
		return members, nil
	}
	for _, member := range vault.Membership {
		if member != "" {
			members = append(members, common.PubKey(member))
		}
	}
	return members, nil
}

// GetKeysignPolicy returns an all-members policy so tests can focus on failure
// reporting without introducing signer filtering as another variable.
func (b *keysignFailureBridge) GetKeysignPolicy(vaultPubKey common.PubKey, blockHeight int64) (thorclient.KeysignPolicy, error) {
	if b.keysignPolicies != nil {
		if policy, ok := b.keysignPolicies[blockHeight]; ok {
			return policy, nil
		}
	}
	signers, err := b.GetKeysignParty(vaultPubKey, blockHeight)
	if err != nil {
		return thorclient.KeysignPolicy{}, err
	}
	return thorclient.KeysignPolicy{PubKey: vaultPubKey, Signers: signers, RequiredSigners: len(signers), ActiveSignerCount: len(signers)}, nil
}

func (b *keysignFailureBridge) GetThorchainVersion() (semver.Version, error) {
	return semver.MustParse("1.134.0"), nil
}

func (b *keysignFailureBridge) GetMimir(key string) (int64, error) {
	return -1, nil
}

func (b *keysignFailureBridge) PostKeysignFailure(
	blame ttypes.Blame,
	height int64,
	memo string,
	coins common.Coins,
	pubkey common.PubKey,
) (common.TxID, error) {
	b.calls++
	b.lastBlame = blame
	b.lastHeight = height
	b.lastMemo = memo
	b.lastCoins = coins
	b.lastPubKey = pubkey
	if b.retErr != nil {
		return "", b.retErr
	}
	return common.TxID("XMR_TEST_KEYSIGN_FAILURE_TX"), nil
}

func newTestSignerCacheManager(t *testing.T) *signercache.CacheManager {
	t.Helper()

	memStorage := storage.NewMemStorage()
	db, err := leveldb.Open(memStorage, nil)
	if err != nil {
		t.Fatalf("open in-memory leveldb: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	signerCache, err := signercache.NewSignerCacheManager(db)
	if err != nil {
		t.Fatalf("new signer cache manager: %v", err)
	}
	return signerCache
}

func newClosedTestSignerCacheManager(t *testing.T) *signercache.CacheManager {
	t.Helper()

	memStorage := storage.NewMemStorage()
	db, err := leveldb.Open(memStorage, nil)
	if err != nil {
		t.Fatalf("open in-memory leveldb: %v", err)
	}
	signerCache, err := signercache.NewSignerCacheManager(db)
	if err != nil {
		t.Fatalf("new signer cache manager: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close in-memory leveldb: %v", err)
	}
	return signerCache
}

func newTestSignedTxPayloadStore(t *testing.T) *signedTxPayloadStore {
	t.Helper()

	memStorage := storage.NewMemStorage()
	db, err := leveldb.Open(memStorage, nil)
	if err != nil {
		t.Fatalf("open in-memory leveldb: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return newSignedTxPayloadStore(db)
}

type mockFrostTssServer struct {
	tssp.Server
	lastReq tssp.FrostKeySignRequest
	resp    tssp.FrostKeySignResponse
	err     error
	fn      func(tssp.FrostKeySignRequest) (tssp.FrostKeySignResponse, error)
}

func (m *mockFrostTssServer) FrostKeySign(req tssp.FrostKeySignRequest, _ tssp.FrostSidecarClient) (tssp.FrostKeySignResponse, error) {
	m.lastReq = req
	if m.fn != nil {
		return m.fn(req)
	}
	return m.resp, m.err
}

type blockingFrostTssServer struct {
	tssp.Server

	calls         int32
	firstEntered  chan struct{}
	secondEntered chan struct{}
	releaseFirst  chan struct{}
	onceFirst     sync.Once
	onceSecond    sync.Once
}

func newBlockingFrostTssServer() *blockingFrostTssServer {
	return &blockingFrostTssServer{
		firstEntered:  make(chan struct{}),
		secondEntered: make(chan struct{}),
		releaseFirst:  make(chan struct{}),
	}
}

func (m *blockingFrostTssServer) FrostKeySign(req tssp.FrostKeySignRequest, _ tssp.FrostSidecarClient) (tssp.FrostKeySignResponse, error) {
	call := atomic.AddInt32(&m.calls, 1)
	switch call {
	case 1:
		m.onceFirst.Do(func() { close(m.firstEntered) })
		<-m.releaseFirst
		return tssp.FrostKeySignResponse{}, errors.New("first frost execution stopped")
	default:
		m.onceSecond.Do(func() { close(m.secondEntered) })
		return tssp.FrostKeySignResponse{Status: gotsscommon.NA}, nil
	}
}

func TestSignTxSerializesFrostSessionsWithoutClearingSidecar(t *testing.T) {
	tssServer := newBlockingFrostTssServer()
	c, tx, server, clearHits := newFrostSignTxCleanupFixture(t, (*mockFrostTssServer)(nil), 0)
	defer server.Close()
	c.tssServer = tssServer

	firstDone := make(chan error, 1)
	go func() {
		_, _, _, err := c.signTx(tx, 12345)
		firstDone <- err
	}()

	select {
	case <-tssServer.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first FROST keysign did not start")
	}

	secondDone := make(chan error, 1)
	go func() {
		secondTx := tx
		secondTx.SigningID = "second-xmr-signing"
		_, _, _, err := c.signTx(secondTx, 12345)
		secondDone <- err
	}()

	select {
	case <-tssServer.secondEntered:
		t.Fatal("second FROST keysign entered while first session was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(tssServer.releaseFirst)

	if err := <-firstDone; err == nil || !strings.Contains(err.Error(), "first frost execution stopped") {
		t.Fatalf("expected first signing failure, got %v", err)
	}
	if got := atomic.LoadInt32(clearHits); got != 0 {
		t.Fatalf("ambiguous FROST failure must retain sidecar sessions, got %d clears", got)
	}

	select {
	case <-tssServer.secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second FROST keysign did not start after first attempt returned")
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("expected second signing to stand down cleanly, got %v", err)
	}
}

func newFrostSignTxCleanupFixture(t *testing.T, tssServer *mockFrostTssServer, clearStatus int) (*Client, stypes.TxOutItem, *httptest.Server, *int32) {
	t.Helper()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	bridge := &keysignFailureBridge{
		vaults: map[string]ttypes.Vault{
			vaultPubKey.String(): {
				PubKey:      vaultPubKey,
				PubKeyFrost: vaultPubKey,
				Status:      ttypes.VaultStatus_ActiveVault,
				StatusSince: 12000,
				Membership:  []string{"member-a"},
			},
		},
	}

	// Signability is verified against the sidecar's recorded group key, so the
	// fake key record must carry the real derived group key, not just the pubkey.
	groupKeyHex, err := groupKeyHexFromVaultPubKey(vaultPubKey)
	if err != nil {
		t.Fatalf("derive group key: %v", err)
	}

	var clearHits int32
	balanceAtomic := uint64(12 * moneroAtomicToThorchain)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/monero/keys" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"vaults": []sidecar.VaultKey{{
					VaultPubKey:  vaultPubKey.String(),
					GroupKey:     groupKeyHex,
					HasFrostKeys: true,
					Signable:     true,
				}},
			})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register" {
			// Registration must carry scanner continuity or the client rejects it.
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(1))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/monero/sessions/clear" {
			atomic.AddInt32(&clearHits, 1)
			if clearStatus >= 400 {
				http.Error(w, "clear failed", clearStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"cleared_sessions": 2,
				"status":           "ok",
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/monero/balance/") {
			_ = json.NewEncoder(w).Encode(sidecar.Balance{
				Balance:          balanceAtomic,
				UnlockedBalance:  balanceAtomic,
				SpendableBalance: uint64Ptr(balanceAtomic),
			})
			return
		}
		http.NotFound(w, r)
	}))

	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		server.Close()
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:                common.XMRChain,
		localNodePubKey:      common.PubKey("member-a"),
		sidecar:              frostSigner,
		tssServer:            tssServer,
		signerCacheManager:   newTestSignerCacheManager(t),
		signedTxPayloadStore: newTestSignedTxPayloadStore(t),
		thorchainBridge:      bridge,
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddress,
		},
	}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		VaultPubKeyFrost: vaultPubKey,
		ToAddress:        testOutboundXMRDestination(),
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	return c, tx, server, &clearHits
}

func successfulFrostResponseForReq(t *testing.T, req tssp.FrostKeySignRequest) tssp.FrostKeySignResponse {
	t.Helper()

	msgID, err := tssp.FrostKeySignMsgID(req)
	if err != nil {
		t.Fatalf("build msg id: %v", err)
	}
	planHash, err := tssp.FrostPlanHash(req, msgID)
	if err != nil {
		t.Fatalf("build plan hash: %v", err)
	}
	txHash := strings.Repeat("1", 64)
	txHex := "abcdef"
	feeAtomic := uint64(1000)
	txKey := strings.Repeat("2", 64)
	eventuality, err := tssp.BuildFrostEventuality(planHash, txHash, txHex, txKey, feeAtomic)
	if err != nil {
		t.Fatalf("build eventuality: %v", err)
	}

	return tssp.FrostKeySignResponse{
		Status:      gotsscommon.Success,
		MsgID:       msgID,
		TxHash:      txHash,
		TxHex:       txHex,
		TxKey:       txKey,
		FeeAtomic:   feeAtomic,
		Eventuality: eventuality,
	}
}

func requireTerminalFrostResultError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected terminal FROST result error")
	}
	var keysignErr bifrosttss.KeysignError
	if !errors.As(err, &keysignErr) {
		t.Fatalf("expected error to expose KeysignError, got %T %v", err, err)
	}
	if keysignErr.Blame.Round != tssMessages.FROSTKEYSIGNRESULT || !keysignErr.IsTerminalRound() {
		t.Fatalf("expected terminal %s round, got %q", tssMessages.FROSTKEYSIGNRESULT, keysignErr.Blame.Round)
	}
	if len(keysignErr.Blame.BlameNodes) != 0 {
		t.Fatalf("local post-result failure must not blame peers: %+v", keysignErr.Blame.BlameNodes)
	}
}

func testKeysignPolicy(vaultPubKey common.PubKey, signers common.PubKeys) thorclient.KeysignPolicy {
	return thorclient.KeysignPolicy{
		PubKey:            vaultPubKey,
		Signers:           signers,
		RequiredSigners:   len(signers),
		ActiveSignerCount: len(signers),
	}
}

// TestSignTxRetainsSidecarSessionsOnFrostKeySignError prevents an ambiguous
// post-share failure from releasing inputs that may back a valid transaction.
func TestSignTxRetainsSidecarSessionsOnFrostKeySignError(t *testing.T) {
	tssErr := errors.New("frost execution stopped")
	tssServer := &mockFrostTssServer{err: tssErr}
	c, tx, server, clearHits := newFrostSignTxCleanupFixture(t, tssServer, 0)
	defer server.Close()

	_, _, _, signErr := c.signTx(tx, 12345)
	if !errors.Is(signErr, tssErr) {
		t.Fatalf("expected frost execution error, got %T %v", signErr, signErr)
	}
	if got := atomic.LoadInt32(clearHits); got != 0 {
		t.Fatalf("expected no clear sessions hit, got %d", got)
	}
}

// TestSignTxRetainsSidecarSessionsOnNonSuccessWithBlame keeps signer-blame
// behavior intact without treating blame as proof that no share escaped.
func TestSignTxRetainsSidecarSessionsOnNonSuccessWithBlame(t *testing.T) {
	tssServer := &mockFrostTssServer{
		resp: tssp.FrostKeySignResponse{
			Status: gotsscommon.Fail,
			Blame: gotssblame.Blame{
				FailReason: "xmr keysign failed",
				BlameNodes: []gotssblame.Node{
					{Pubkey: "thorpub1blamenode"},
				},
			},
		},
	}
	c, tx, server, clearHits := newFrostSignTxCleanupFixture(t, tssServer, http.StatusInternalServerError)
	defer server.Close()

	_, _, _, signErr := c.signTx(tx, 12345)
	var keysignErr bifrosttss.KeysignError
	if !errors.As(signErr, &keysignErr) {
		t.Fatalf("expected keysign error despite clear failure, got %T %v", signErr, signErr)
	}
	if got := atomic.LoadInt32(clearHits); got != 0 {
		t.Fatalf("expected no clear sessions hit, got %d", got)
	}
}

// TestSignTxDoesNotClearSidecarSessionsOnFrostStandDown covers the selected
// stand-down response. No sidecar session is abandoned in this case, so there
// is no reservation to roll back.
func TestSignTxDoesNotClearSidecarSessionsOnFrostStandDown(t *testing.T) {
	tssServer := &mockFrostTssServer{
		resp: tssp.FrostKeySignResponse{Status: gotsscommon.NA},
	}
	c, tx, server, clearHits := newFrostSignTxCleanupFixture(t, tssServer, 0)
	defer server.Close()

	_, _, _, signErr := c.signTx(tx, 12345)
	if signErr != nil {
		t.Fatalf("expected stand-down without error, got %v", signErr)
	}
	if got := atomic.LoadInt32(clearHits); got != 0 {
		t.Fatalf("expected no clear sessions hit, got %d", got)
	}
}

// TestSignTxRetainsSidecarSessionsOnEmptyMsgID ensures malformed successful
// responses cannot release potentially used inputs.
func TestSignTxRetainsSidecarSessionsOnEmptyMsgID(t *testing.T) {
	tssServer := &mockFrostTssServer{
		resp: tssp.FrostKeySignResponse{Status: gotsscommon.Success},
	}
	c, tx, server, clearHits := newFrostSignTxCleanupFixture(t, tssServer, 0)
	defer server.Close()

	_, _, _, signErr := c.signTx(tx, 12345)
	if signErr == nil || !strings.Contains(signErr.Error(), "empty msg_id") {
		t.Fatalf("expected empty msg_id error, got %T %v", signErr, signErr)
	}
	requireTerminalFrostResultError(t, signErr)
	if got := atomic.LoadInt32(clearHits); got != 0 {
		t.Fatalf("expected no clear sessions hit, got %d", got)
	}
}

func TestSignTxRetainsSidecarSessionsOnInvalidEventuality(t *testing.T) {
	tssServer := &mockFrostTssServer{
		fn: func(req tssp.FrostKeySignRequest) (tssp.FrostKeySignResponse, error) {
			resp := successfulFrostResponseForReq(t, req)
			resp.Eventuality.PlanHash = "not-the-plan"
			return resp, nil
		},
	}
	c, tx, server, clearHits := newFrostSignTxCleanupFixture(t, tssServer, 0)
	defer server.Close()

	_, _, _, signErr := c.signTx(tx, 12345)
	if signErr == nil || !strings.Contains(signErr.Error(), "invalid FROST eventuality") {
		t.Fatalf("expected invalid eventuality error, got %T %v", signErr, signErr)
	}
	requireTerminalFrostResultError(t, signErr)
	if got := atomic.LoadInt32(clearHits); got != 0 {
		t.Fatalf("expected no clear sessions hit, got %d", got)
	}
}

func TestSignTxDoesNotClearSidecarSessionsOnSuccessfulSign(t *testing.T) {
	tssServer := &mockFrostTssServer{
		fn: func(req tssp.FrostKeySignRequest) (tssp.FrostKeySignResponse, error) {
			return successfulFrostResponseForReq(t, req), nil
		},
	}
	c, tx, server, clearHits := newFrostSignTxCleanupFixture(t, tssServer, 0)
	defer server.Close()

	signedTx, _, _, signErr := c.signTx(tx, 12345)
	if signErr != nil {
		t.Fatalf("expected successful sign, got %v", signErr)
	}
	if len(signedTx) == 0 {
		t.Fatalf("expected signed tx payload")
	}
	if got := atomic.LoadInt32(clearHits); got != 0 {
		t.Fatalf("expected no clear sessions hit, got %d", got)
	}
}

// TestSignTxReusesCachedPayloadAfterValidatedFrostSuccess verifies that a
// successful FROST result becomes the durable pre-broadcast retry artifact.
func TestSignTxReusesCachedPayloadAfterValidatedFrostSuccess(t *testing.T) {
	var calls int32
	tssServer := &mockFrostTssServer{
		fn: func(req tssp.FrostKeySignRequest) (tssp.FrostKeySignResponse, error) {
			atomic.AddInt32(&calls, 1)
			return successfulFrostResponseForReq(t, req), nil
		},
	}
	c, tx, server, clearHits := newFrostSignTxCleanupFixture(t, tssServer, 0)
	defer server.Close()

	// The first signing round should persist the payload but leave signer cache
	// untouched until BroadcastTx accepts a chain tx hash.
	signedTx, _, _, signErr := c.signTx(tx, 12345)
	if signErr != nil {
		t.Fatalf("expected successful sign, got %v", signErr)
	}
	if len(signedTx) == 0 {
		t.Fatalf("expected signed tx payload")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected one FROST keysign call, got %d", got)
	}
	if c.signerCacheManager.HasSigned(tx.CacheHash()) {
		t.Fatal("expected FROST success to leave signer cache for BroadcastTx")
	}
	if got := atomic.LoadInt32(clearHits); got != 0 {
		t.Fatalf("expected no clear sessions hit, got %d", got)
	}

	// A retry before broadcast must reuse the exact payload and not re-enter
	// FROST with a potentially different input/ref set.
	secondSignedTx, secondCheckpoint, secondObservation, secondErr := c.signTx(tx, 12346)
	if secondErr != nil {
		t.Fatalf("expected cached retry to reuse payload without error, got %v", secondErr)
	}
	if !bytes.Equal(secondSignedTx, signedTx) {
		t.Fatalf("expected cached retry to return the first signed tx payload")
	}
	if len(secondCheckpoint) == 0 {
		t.Fatalf("expected cached retry to return checkpoint")
	}
	if secondObservation == nil {
		t.Fatalf("expected cached retry to return outbound observation")
	}
	if c.signerCacheManager.HasSigned(tx.CacheHash()) {
		t.Fatal("expected cached payload recovery not to restore signer cache before broadcast")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected cached retry not to call FROST keysign again, got %d calls", got)
	}
}

// TestSignTxRecoversSignedPayloadAcrossRescheduledFrostVaults protects against
// a reschedule authorizing a second FROST ceremony after the original vault
// already produced a complete signed transaction.
func TestSignTxRecoversSignedPayloadAcrossRescheduledFrostVaults(t *testing.T) {
	var calls int32
	tssServer := &mockFrostTssServer{
		fn: func(req tssp.FrostKeySignRequest) (tssp.FrostKeySignResponse, error) {
			atomic.AddInt32(&calls, 1)
			return successfulFrostResponseForReq(t, req), nil
		},
	}
	c, tx, server, _ := newFrostSignTxCleanupFixture(t, tssServer, 0)
	defer server.Close()

	firstSignedTx, _, _, signErr := c.signTx(tx, 12345)
	if signErr != nil {
		t.Fatalf("expected first vault sign to succeed, got %v", signErr)
	}
	if len(firstSignedTx) == 0 {
		t.Fatalf("expected first vault signed payload")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected first FROST keysign call, got %d", got)
	}

	// The second outbound keeps the same logical identity but switches vaults.
	// It must recover the exact old-vault transaction rather than sign again.
	secondVaultPubKey := testVaultPubKey(t, t.Name()+":second-vault")
	secondVaultAddress, err := secondVaultPubKey.GetAddress(common.XMRChain)
	if err != nil {
		t.Fatalf("derive second vault address: %v", err)
	}
	bridge, ok := c.thorchainBridge.(*keysignFailureBridge)
	if !ok {
		t.Fatalf("expected keysign failure bridge")
	}
	bridge.vaults[secondVaultPubKey.String()] = ttypes.Vault{
		PubKey:      secondVaultPubKey,
		PubKeyFrost: secondVaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 12000,
		Membership:  []string{"member-a"},
	}
	c.vaultAddressMap[secondVaultPubKey.String()] = secondVaultAddress
	c.signerCacheManager = newTestSignerCacheManager(t)

	secondTx := tx
	secondTx.VaultPubKey = secondVaultPubKey
	secondTx.VaultPubKeyEddsa = secondVaultPubKey
	secondTx.VaultPubKeyFrost = secondVaultPubKey
	if secondTx.CacheHash() != tx.CacheHash() {
		t.Fatalf("test setup expected same cache hash across vaults")
	}

	secondSignedTx, _, _, secondErr := c.signTx(secondTx, 12346)
	if secondErr != nil {
		t.Fatalf("expected second vault sign to succeed, got %v", secondErr)
	}
	if len(secondSignedTx) == 0 {
		t.Fatalf("expected second vault signed payload")
	}
	if !bytes.Equal(secondSignedTx, firstSignedTx) {
		t.Fatal("expected rescheduled vault to recover the original signed payload")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected rescheduled vault not to start a second FROST ceremony, got %d calls", got)
	}
}

// TestSignTxSuppressesSecondFrostRoundWithDifferentActiveSetAndWithholding
// models the work-item attack: a second active set with withholding signers
// must not get a chance to create a different XMR transaction.
func TestSignTxSuppressesSecondFrostRoundWithDifferentActiveSetAndWithholding(t *testing.T) {
	// Two overlapping 7-of-10 active sets make the retry look valid locally
	// unless the cached signed payload suppresses a second FROST ceremony.
	members := common.PubKeys{
		common.PubKey("member-a"),
		common.PubKey("member-b"),
		common.PubKey("member-c"),
		common.PubKey("member-d"),
		common.PubKey("member-e"),
		common.PubKey("member-f"),
		common.PubKey("member-g"),
		common.PubKey("member-h"),
		common.PubKey("member-i"),
		common.PubKey("member-j"),
	}
	firstActive := common.PubKeys{members[0], members[1], members[2], members[3], members[4], members[5], members[6]}
	secondActive := common.PubKeys{members[3], members[4], members[5], members[6], members[7], members[8], members[9]}
	withholdingBlame := []gotssblame.Node{
		{Pubkey: members[3].String()},
		{Pubkey: members[4].String()},
		{Pubkey: members[5].String()},
		{Pubkey: members[6].String()},
	}

	var calls int32
	tssServer := &mockFrostTssServer{
		fn: func(req tssp.FrostKeySignRequest) (tssp.FrostKeySignResponse, error) {
			call := atomic.AddInt32(&calls, 1)
			if call == 1 {
				if got, want := strings.Join(req.ActiveSignerPubKeys, ","), strings.Join(firstActive.Strings(), ","); got != want {
					t.Fatalf("expected first active set %s, got %s", want, got)
				}
				return successfulFrostResponseForReq(t, req), nil
			}
			if got, want := strings.Join(req.ActiveSignerPubKeys, ","), strings.Join(secondActive.Strings(), ","); got != want {
				t.Fatalf("expected second active set %s, got %s", want, got)
			}
			return tssp.FrostKeySignResponse{
				Status: gotsscommon.Fail,
				Blame: gotssblame.Blame{
					FailReason: "four active signers withheld",
					BlameNodes: withholdingBlame,
				},
			}, nil
		},
	}

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	bridge := &keysignFailureBridge{
		vaults: map[string]ttypes.Vault{
			vaultPubKey.String(): {
				PubKey:      vaultPubKey,
				PubKeyFrost: vaultPubKey,
				Status:      ttypes.VaultStatus_ActiveVault,
				StatusSince: 12000,
				Membership:  members.Strings(),
			},
		},
		keysignPolicies: map[int64]thorclient.KeysignPolicy{
			12345: testKeysignPolicy(vaultPubKey, firstActive),
			12346: testKeysignPolicy(vaultPubKey, secondActive),
		},
	}
	balanceAtomic := uint64(12 * moneroAtomicToThorchain)
	balanceServer := newFrostBalanceServer(t, map[string]sidecar.Balance{
		vaultPubKey.String(): {
			Balance:          balanceAtomic,
			UnlockedBalance:  balanceAtomic,
			SpendableBalance: uint64Ptr(balanceAtomic),
		},
	})
	defer balanceServer.Close()
	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:                common.XMRChain,
		localNodePubKey:      members[0],
		sidecar:              frostSigner,
		tssServer:            tssServer,
		signerCacheManager:   newTestSignerCacheManager(t),
		signedTxPayloadStore: newTestSignedTxPayloadStore(t),
		thorchainBridge:      bridge,
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddress,
		},
	}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		VaultPubKeyFrost: vaultPubKey,
		ToAddress:        testOutboundXMRDestination(),
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	signedTx, _, _, signErr := c.signTx(tx, 12345)
	if signErr != nil {
		t.Fatalf("expected first FROST round to succeed, got %v", signErr)
	}
	if len(signedTx) == 0 {
		t.Fatalf("expected first signed tx payload")
	}

	// Reset signer cache to simulate a restart before broadcast; the payload
	// store should still block the second active set from signing.
	c.signerCacheManager = newTestSignerCacheManager(t)
	secondSignedTx, secondCheckpoint, secondObservation, secondErr := c.signTx(tx, 12346)
	if secondErr != nil {
		t.Fatalf("expected cached payload to suppress second FROST round, got %v", secondErr)
	}
	if !bytes.Equal(secondSignedTx, signedTx) {
		t.Fatalf("expected retry to reuse the first signed tx payload")
	}
	if len(secondCheckpoint) == 0 {
		t.Fatalf("expected cached retry to return checkpoint")
	}
	if secondObservation == nil {
		t.Fatalf("expected cached retry to return outbound observation")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected second 7-of-10 active set not to enter FROST, got %d calls", got)
	}
}

// TestSignTxDoesNotRequireSignerCacheWriteBeforeBroadcast ensures FROST
// success is not coupled to signer cache writes that only become meaningful
// after broadcast.
func TestSignTxDoesNotRequireSignerCacheWriteBeforeBroadcast(t *testing.T) {
	var calls int32
	tssServer := &mockFrostTssServer{
		fn: func(req tssp.FrostKeySignRequest) (tssp.FrostKeySignResponse, error) {
			atomic.AddInt32(&calls, 1)
			return successfulFrostResponseForReq(t, req), nil
		},
	}
	c, tx, server, clearHits := newFrostSignTxCleanupFixture(t, tssServer, 0)
	defer server.Close()
	// Close the signer cache to prove signTx no longer writes it during FROST
	// success; only the signed payload store is required at this stage.
	c.signerCacheManager = newClosedTestSignerCacheManager(t)

	signedTx, checkpoint, observation, signErr := c.signTx(tx, 12345)
	if signErr != nil {
		t.Fatalf("expected signer cache write not to be required before broadcast, got %v", signErr)
	}
	if len(signedTx) == 0 || len(checkpoint) == 0 || observation == nil {
		t.Fatalf("expected signing artifacts before broadcast signer-cache write")
	}
	if got := atomic.LoadInt32(clearHits); got != 0 {
		t.Fatalf("expected no clear sessions hit, got %d", got)
	}

	c.signerCacheManager = newTestSignerCacheManager(t)
	recoveredTx, recoveredCheckpoint, recoveredObservation, recoveredErr := c.signTx(tx, 12346)
	if recoveredErr != nil {
		t.Fatalf("expected persisted payload recovery, got %v", recoveredErr)
	}
	if len(recoveredTx) == 0 || len(recoveredCheckpoint) == 0 || recoveredObservation == nil {
		t.Fatalf("expected recovered signing artifacts")
	}
	if c.signerCacheManager.HasSigned(tx.CacheHash()) {
		t.Fatal("expected recovered payload not to restore signer cache before broadcast")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected recovery not to call FROST keysign again, got %d calls", got)
	}
}

// TestSignTxRecoversSignedPayloadBeforeSignerCacheSuppression covers the
// post-broadcast XMR retry shape: BroadcastTx has already marked signer cache,
// but spent-ref import has not yet become durable, so the signed payload must
// still be recovered and rebroadcast instead of suppressing signing entirely.
func TestSignTxRecoversSignedPayloadBeforeSignerCacheSuppression(t *testing.T) {
	var calls int32
	tssServer := &mockFrostTssServer{
		fn: func(req tssp.FrostKeySignRequest) (tssp.FrostKeySignResponse, error) {
			atomic.AddInt32(&calls, 1)
			return successfulFrostResponseForReq(t, req), nil
		},
	}
	c, tx, server, clearHits := newFrostSignTxCleanupFixture(t, tssServer, 0)
	defer server.Close()

	signedTx, checkpoint, observation, signErr := c.signTx(tx, 12345)
	if signErr != nil {
		t.Fatalf("expected initial sign to persist payload, got %v", signErr)
	}
	if len(signedTx) == 0 || len(checkpoint) == 0 || observation == nil {
		t.Fatalf("expected initial signing artifacts")
	}

	// Simulate the state after BroadcastTx succeeds but before spent refs are
	// imported. The signer cache says the outbound has a tx hash, but the
	// payload store is still the artifact needed for an actual retry.
	cacheHash := tx.CacheHash()
	cacheVault := txCacheVault(tx, c.GetChain())
	if err := c.signerCacheManager.SetSigned(cacheHash, cacheVault, observation.Tx); err != nil {
		t.Fatalf("set signer cache: %v", err)
	}

	recoveredTx, recoveredCheckpoint, recoveredObservation, recoveredErr := c.signTx(tx, 12346)
	if recoveredErr != nil {
		t.Fatalf("expected signed payload recovery before signer-cache suppression, got %v", recoveredErr)
	}
	if len(recoveredTx) == 0 || len(recoveredCheckpoint) == 0 || recoveredObservation == nil {
		t.Fatalf("expected recovered signing artifacts")
	}
	if recoveredObservation.Tx != observation.Tx {
		t.Fatalf("expected recovered tx hash %q, got %q", observation.Tx, recoveredObservation.Tx)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected recovery not to call FROST keysign again, got %d calls", got)
	}
	if got := atomic.LoadInt32(clearHits); got != 0 {
		t.Fatalf("expected no clear sessions hit, got %d", got)
	}
}

// TestSignTxFailsBeforeSignerCacheWhenSignedPayloadPersistenceFails prevents a
// node from claiming an outbound is safely signed when it failed to persist the
// retryable XMR proof bundle.
func TestSignTxFailsBeforeSignerCacheWhenSignedPayloadPersistenceFails(t *testing.T) {
	var c *Client
	tssServer := &mockFrostTssServer{
		fn: func(req tssp.FrostKeySignRequest) (tssp.FrostKeySignResponse, error) {
			// Fail the local durability layer only after the coordinator has
			// produced a valid signed result.
			if err := c.signedTxPayloadStore.db.Close(); err != nil {
				t.Fatalf("close signed payload store after FROST success: %v", err)
			}
			return successfulFrostResponseForReq(t, req), nil
		},
	}
	var tx stypes.TxOutItem
	var server *httptest.Server
	var clearHits *int32
	c, tx, server, clearHits = newFrostSignTxCleanupFixture(t, tssServer, 0)
	defer server.Close()

	signedTx, checkpoint, observation, signErr := c.signTx(tx, 12345)
	if signErr == nil || !strings.Contains(signErr.Error(), "reserve successful xmr FROST result before persistence") {
		t.Fatalf("expected signed-payload persistence failure, got %T %v", signErr, signErr)
	}
	requireTerminalFrostResultError(t, signErr)
	if len(signedTx) != 0 || len(checkpoint) != 0 || observation != nil {
		t.Fatalf("expected failed persistence to return no signing artifacts")
	}
	if c.signerCacheManager.HasSigned(tx.CacheHash()) {
		t.Fatal("expected signer cache not to be marked when signed payload was not persisted")
	}
	if got := atomic.LoadInt32(clearHits); got != 0 {
		t.Fatalf("expected no clear sessions hit, got %d", got)
	}
}

func TestSignTxPassesXMRDustThresholdInAtomicUnits(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	bridge := &keysignFailureBridge{
		vaults: map[string]ttypes.Vault{
			vaultPubKey.String(): {
				PubKey:      vaultPubKey,
				PubKeyFrost: vaultPubKey,
				Status:      ttypes.VaultStatus_ActiveVault,
				StatusSince: 12000,
				Membership:  []string{"member-a"},
			},
		},
	}
	tssServer := &mockFrostTssServer{err: errors.New("stop after request capture")}
	balanceServer := newFrostBalanceServer(t, map[string]sidecar.Balance{
		vaultPubKey.String(): {
			Balance:         12 * moneroAtomicToThorchain,
			UnlockedBalance: 12 * moneroAtomicToThorchain,
		},
	})
	defer balanceServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:                common.XMRChain,
		localNodePubKey:      common.PubKey("member-a"),
		sidecar:              frostSigner,
		tssServer:            tssServer,
		signerCacheManager:   newTestSignerCacheManager(t),
		signedTxPayloadStore: newTestSignedTxPayloadStore(t),
		thorchainBridge:      bridge,
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddress,
		},
	}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		VaultPubKeyFrost: vaultPubKey,
		ToAddress:        testOutboundXMRDestination(),
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(2)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	_, _, _, signErr := c.signTx(tx, 12345)
	if signErr == nil {
		t.Fatalf("expected signTx to stop after request capture")
	}

	expected, err := thorchainAmountToAtomic(common.XMRChain.DustThreshold())
	if err != nil {
		t.Fatalf("convert dust threshold: %v", err)
	}
	if expected == common.XMRChain.DustThreshold().Uint64() {
		t.Fatalf("test is not exercising THORChain-to-atomic unit conversion")
	}
	if tssServer.lastReq.DustThreshold != expected {
		t.Fatalf("expected atomic dust threshold %d, got %d", expected, tssServer.lastReq.DustThreshold)
	}
	expectedMaxGas, err := thorchainAmountToAtomic(cosmos.NewUint(2))
	if err != nil {
		t.Fatalf("convert max gas: %v", err)
	}
	if tssServer.lastReq.MaxGasAtomic != expectedMaxGas {
		t.Fatalf("expected atomic max gas %d, got %d", expectedMaxGas, tssServer.lastReq.MaxGasAtomic)
	}
}

// TestSignTxPassesXMRMaxGasInAtomicUnits proves Bifrost sends the scheduled
// MaxGas to the FROST layer in Monero atomic units, matching the signer API.
func TestSignTxPassesXMRMaxGasInAtomicUnits(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	bridge := &keysignFailureBridge{
		vaults: map[string]ttypes.Vault{
			vaultPubKey.String(): {
				PubKey:      vaultPubKey,
				PubKeyFrost: vaultPubKey,
				Status:      ttypes.VaultStatus_ActiveVault,
				StatusSince: 12000,
				Membership:  []string{"member-a"},
			},
		},
	}
	tssServer := &mockFrostTssServer{err: errors.New("stop after request capture")}
	balanceServer := newFrostBalanceServer(t, map[string]sidecar.Balance{
		vaultPubKey.String(): {
			Balance:         10 * moneroAtomicToThorchain,
			UnlockedBalance: 10 * moneroAtomicToThorchain,
		},
	})
	defer balanceServer.Close()

	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:                common.XMRChain,
		localNodePubKey:      common.PubKey("member-a"),
		sidecar:              frostSigner,
		tssServer:            tssServer,
		signerCacheManager:   newTestSignerCacheManager(t),
		signedTxPayloadStore: newTestSignedTxPayloadStore(t),
		thorchainBridge:      bridge,
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddress,
		},
	}
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		VaultPubKeyFrost: vaultPubKey,
		ToAddress:        testOutboundXMRDestination(),
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(2)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	_, _, _, signErr := c.signTx(tx, 12345)
	if signErr == nil {
		t.Fatalf("expected signTx to stop after request capture")
	}

	expected, err := thorchainAmountToAtomic(cosmos.NewUint(2))
	if err != nil {
		t.Fatalf("convert max gas: %v", err)
	}
	if tssServer.lastReq.MaxGasAtomic != expected {
		t.Fatalf("expected MaxGasAtomic=%d, got %d", expected, tssServer.lastReq.MaxGasAtomic)
	}
}

// TestEnforceXMRMaxGasRejectsOverCapFees documents the local compatibility
// guard for signers that return an over-cap fee instead of rejecting it first.
func TestEnforceXMRMaxGasRejectsOverCapFees(t *testing.T) {
	t.Parallel()

	// This helper-level check protects 3.19 Bifrost from accepting an over-cap
	// signed result even when the connected signer does not enforce MaxGas
	// during transaction construction.
	if err := enforceXMRMaxGas(100, 100); err != nil {
		t.Fatalf("expected fee equal to cap to pass: %v", err)
	}
	err := enforceXMRMaxGas(101, 100)
	if err == nil {
		t.Fatal("expected over-cap xmr fee to fail")
	}
	if !strings.Contains(err.Error(), tssp.FrostFailReasonXMRMaxGasExceeded) {
		t.Fatalf("expected stable max gas error prefix, got %v", err)
	}
	var keysignErr bifrosttss.KeysignError
	if !errors.As(err, &keysignErr) || !keysignErr.IsTerminalRound() {
		t.Fatalf("over-cap result must preserve terminal FROST share semantics: %v", err)
	}
}

// TestSignTxTreatsSuccessfulOverCapFrostResultAsTerminal covers the
// compatibility path where an older coordinator returns a signed success and
// Bifrost is the first layer to reject its fee. The concrete fee error must
// remain available for network-fee feedback while the signer sees a terminal,
// blame-free result boundary.
func TestSignTxTreatsSuccessfulOverCapFrostResultAsTerminal(t *testing.T) {
	tssServer := &mockFrostTssServer{
		fn: func(req tssp.FrostKeySignRequest) (tssp.FrostKeySignResponse, error) {
			resp := successfulFrostResponseForReq(t, req)
			resp.FeeAtomic = req.MaxGasAtomic + 1
			var err error
			resp.Eventuality, err = tssp.BuildFrostEventuality(
				resp.Eventuality.PlanHash,
				resp.TxHash,
				resp.TxHex,
				resp.TxKey,
				resp.FeeAtomic,
			)
			if err != nil {
				t.Fatalf("rebuild over-cap eventuality: %v", err)
			}
			return resp, nil
		},
	}
	c, tx, server, _ := newFrostSignTxCleanupFixture(t, tssServer, 0)
	defer server.Close()
	tx.MaxGas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))}

	_, _, _, signErr := c.SignTx(tx, 12345)
	var maxGasErr xmrMaxGasExceededError
	if !errors.As(signErr, &maxGasErr) {
		t.Fatalf("expected typed max gas error, got %T %v", signErr, signErr)
	}
	requireTerminalFrostResultError(t, signErr)
	bridge, ok := c.thorchainBridge.(*keysignFailureBridge)
	if !ok {
		t.Fatal("expected keysign failure bridge")
	}
	if bridge.calls != 0 {
		t.Fatalf("local over-cap result must not post peer blame, got %d calls", bridge.calls)
	}
}

func TestSignTxPostsKeysignFailureWhenSignFails(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	bridge := &keysignFailureBridge{
		vaults: map[string]ttypes.Vault{
			vaultPubKey.String(): {
				PubKey:      vaultPubKey,
				PubKeyFrost: vaultPubKey,
				Status:      ttypes.VaultStatus_ActiveVault,
				StatusSince: 12000,
				Membership:  []string{"member-a"},
			},
		},
	}

	tssServer := &mockFrostTssServer{
		resp: tssp.FrostKeySignResponse{
			Status: gotsscommon.Fail,
			Blame: gotssblame.Blame{
				FailReason: "xmr keysign failed",
				BlameNodes: []gotssblame.Node{
					{Pubkey: "thorpub1blamenode"},
				},
			},
		},
	}
	balanceServer := newFrostBalanceServer(t, map[string]sidecar.Balance{
		vaultPubKey.String(): {
			Balance:         12 * moneroAtomicToThorchain,
			UnlockedBalance: 12 * moneroAtomicToThorchain,
		},
	})
	defer balanceServer.Close()
	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:                common.XMRChain,
		localNodePubKey:      common.PubKey("member-a"),
		sidecar:              frostSigner,
		tssServer:            tssServer,
		signerCacheManager:   newTestSignerCacheManager(t),
		signedTxPayloadStore: newTestSignedTxPayloadStore(t),
		thorchainBridge:      bridge,
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddress,
		},
	}

	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		VaultPubKeyFrost: vaultPubKey,
		ToAddress:        testOutboundXMRDestination(),
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "OUT:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
	}

	_, _, _, signErr := c.SignTx(tx, 12345)
	if signErr == nil {
		t.Fatalf("expected SignTx error")
	}
	var keysignErr bifrosttss.KeysignError
	if !errors.As(signErr, &keysignErr) {
		t.Fatalf("expected keysign error, got: %T %v", signErr, signErr)
	}
	if len(keysignErr.Blame.BlameNodes) != 1 {
		t.Fatalf("expected one blame node on keysign error, got %d", len(keysignErr.Blame.BlameNodes))
	}
	if bridge.calls != 1 {
		t.Fatalf("expected PostKeysignFailure to be called once, got %d", bridge.calls)
	}
}

func TestSignTxReportsNetworkFeeWhenXMRMaxGasExceeded(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	bridge := &keysignFailureBridge{
		vaults: map[string]ttypes.Vault{
			vaultPubKey.String(): {
				PubKey:      vaultPubKey,
				PubKeyFrost: vaultPubKey,
				Status:      ttypes.VaultStatus_ActiveVault,
				StatusSince: 12000,
				Membership:  []string{"member-a"},
			},
		},
	}

	maxGasThorchain := cosmos.NewUint(10)
	maxGasAtomic, err := thorchainAmountToAtomic(maxGasThorchain)
	if err != nil {
		t.Fatalf("convert max gas: %v", err)
	}
	actualFeeAtomic := maxGasAtomic + 25_000
	balanceAtomic := uint64(12 * moneroAtomicToThorchain)
	tssServer := &mockFrostTssServer{
		resp: tssp.FrostKeySignResponse{
			Status:    gotsscommon.Fail,
			FeeAtomic: actualFeeAtomic,
			Blame: gotssblame.Blame{
				FailReason: tssp.FrostFailReasonXMRMaxGasExceeded,
			},
		},
	}
	balanceServer := newFrostBalanceServer(t, map[string]sidecar.Balance{
		vaultPubKey.String(): {
			Balance:          balanceAtomic,
			UnlockedBalance:  balanceAtomic,
			SpendableBalance: uint64Ptr(balanceAtomic),
		},
	})
	defer balanceServer.Close()
	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	feeQueue := make(chan common.NetworkFee, 1)
	c := &Client{
		chain:                 common.XMRChain,
		scanner:               &Scanner{currentBlockHeight: uberatomic.NewInt64(67890)},
		localNodePubKey:       common.PubKey("member-a"),
		sidecar:               frostSigner,
		tssServer:             tssServer,
		signerCacheManager:    newTestSignerCacheManager(t),
		signedTxPayloadStore:  newTestSignedTxPayloadStore(t),
		thorchainBridge:       bridge,
		globalNetworkFeeQueue: feeQueue,
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddress,
		},
	}

	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		VaultPubKeyFrost: vaultPubKey,
		ToAddress:        testOutboundXMRDestination(),
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.XMRAsset, maxGasThorchain),
		},
		Memo: "OUT:2222222222222222222222222222222222222222222222222222222222222222",
	}

	const thorchainTxOutHeight = int64(12345)
	_, _, _, signErr := c.SignTx(tx, thorchainTxOutHeight)
	if signErr == nil {
		t.Fatalf("expected SignTx error")
	}
	var maxGasErr xmrMaxGasExceededError
	if !errors.As(signErr, &maxGasErr) {
		t.Fatalf("expected xmr max gas exceeded error, got %T %v", signErr, signErr)
	}
	requireTerminalFrostResultError(t, signErr)
	if bridge.calls != 0 {
		t.Fatalf("expected no keysign failure post for max gas feedback error, got %d calls", bridge.calls)
	}

	select {
	case got := <-feeQueue:
		if !got.Chain.Equals(common.XMRChain) {
			t.Fatalf("expected XMR network fee, got %s", got.Chain)
		}
		if got.Height != 67890 {
			t.Fatalf("expected processed xmr height 67890, got %d", got.Height)
		}
		if got.Height == thorchainTxOutHeight {
			t.Fatalf("network fee report must not use THORChain txout height %d", thorchainTxOutHeight)
		}
		if got.TransactionSize != typicalVaultOutboundWeight {
			t.Fatalf("expected tx size %d, got %d", typicalVaultOutboundWeight, got.TransactionSize)
		}
		expectedRate := feeRateFromTotalAtomic(actualFeeAtomic)
		if got.TransactionRate != expectedRate {
			t.Fatalf("expected fee rate %d, got %d", expectedRate, got.TransactionRate)
		}
	default:
		t.Fatal("expected xmr max gas exceeded network fee report")
	}
}

func TestReportXMRMaxGasExceededRequiresProcessedXMRHeight(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		scanner *Scanner
	}{
		{name: "scanner unavailable"},
		{
			name:    "scanner has not processed a block",
			scanner: &Scanner{currentBlockHeight: uberatomic.NewInt64(0)},
		},
		{
			name:    "scanner height is invalid",
			scanner: &Scanner{currentBlockHeight: uberatomic.NewInt64(-1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			feeQueue := make(chan common.NetworkFee, 1)
			client := &Client{
				chain:                 common.XMRChain,
				scanner:               tt.scanner,
				globalNetworkFeeQueue: feeQueue,
			}
			client.reportXMRMaxGasExceeded(xmrMaxGasExceededError{
				actualFeeAtomic: 2,
				maxGasAtomic:    1,
			})

			select {
			case fee := <-feeQueue:
				t.Fatalf("unexpected network fee report at height %d", fee.Height)
			default:
			}
		})
	}
}

func TestCanonicalXMRNetworkFeeReportHeightCoalescesNearbyScanners(t *testing.T) {
	t.Parallel()

	if first, second := canonicalXMRNetworkFeeReportHeight(67890), canonicalXMRNetworkFeeReportHeight(67894); first != 67890 || second != first {
		t.Fatalf("nearby scanner heights did not share a real XMR report height: first=%d second=%d", first, second)
	}
	if got := canonicalXMRNetworkFeeReportHeight(67899); got != 67890 {
		t.Fatalf("height immediately below boundary mapped to %d", got)
	}
	if got := canonicalXMRNetworkFeeReportHeight(67900); got != 67900 {
		t.Fatalf("bucket boundary mapped to %d", got)
	}
	for _, height := range []int64{-1, 0, 1, 9} {
		if got := canonicalXMRNetworkFeeReportHeight(height); got != 0 {
			t.Fatalf("non-reportable height %d mapped to %d", height, got)
		}
	}
}

func TestSignTxPreservesEmptyBlameTerminalRoundWithoutPosting(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	bridge := &keysignFailureBridge{
		vaults: map[string]ttypes.Vault{
			vaultPubKey.String(): {
				PubKey:      vaultPubKey,
				PubKeyFrost: vaultPubKey,
				Status:      ttypes.VaultStatus_ActiveVault,
				StatusSince: 900,
				Membership:  []string{"member-a"},
			},
		},
	}

	tssServer := &mockFrostTssServer{
		resp: tssp.FrostKeySignResponse{
			Status: gotsscommon.Fail,
			Blame: gotssblame.Blame{
				FailReason: "xmr keysign failed",
				Round:      tssMessages.FROSTKEYSIGNSHARE,
			},
		},
	}
	balanceServer := newFrostBalanceServer(t, map[string]sidecar.Balance{
		vaultPubKey.String(): {
			Balance:         10 * moneroAtomicToThorchain,
			UnlockedBalance: 10 * moneroAtomicToThorchain,
		},
	})
	defer balanceServer.Close()
	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, balanceServer.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		chain:                common.XMRChain,
		localNodePubKey:      common.PubKey("member-a"),
		sidecar:              frostSigner,
		tssServer:            tssServer,
		signerCacheManager:   newTestSignerCacheManager(t),
		signedTxPayloadStore: newTestSignedTxPayloadStore(t),
		thorchainBridge:      bridge,
		vaultAddressMap: map[string]common.Address{
			vaultPubKey.String(): vaultAddress,
		},
	}

	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		VaultPubKey:      vaultPubKey,
		VaultPubKeyEddsa: vaultPubKey,
		VaultPubKeyFrost: vaultPubKey,
		ToAddress:        testOutboundXMRDestination(),
		Coins: common.Coins{
			common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
		},
		Memo: "OUT:1111111111111111111111111111111111111111111111111111111111111111",
	}

	_, _, _, signErr := c.SignTx(tx, 999)
	if signErr == nil {
		t.Fatalf("expected SignTx error")
	}
	var keysignErr bifrosttss.KeysignError
	if !errors.As(signErr, &keysignErr) {
		t.Fatalf("expected recognized empty-blame round to remain a keysign error, got %T %v", signErr, signErr)
	}
	if !keysignErr.IsTerminalRound() {
		t.Fatalf("expected %s to remain locally terminal", keysignErr.Blame.Round)
	}

	if bridge.calls != 0 {
		t.Fatalf("expected PostKeysignFailure not to be called, got %d calls", bridge.calls)
	}
}
