package monero

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blang/semver"
	"github.com/rs/zerolog/log"
	"go.uber.org/atomic"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	tsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type consolidationBridge struct {
	thorclient.ThorchainBridge
	nodeStatus  ttypes.NodeStatus
	blockHeight int64
	asgards     ttypes.Vaults
	mimir       map[string]int64
	constants   map[string]int64
	mu          sync.Mutex
	mimirCalls  int
	asgardCalls int
}

func (b *consolidationBridge) FetchNodeStatus() (ttypes.NodeStatus, error) {
	return b.nodeStatus, nil
}

func (b *consolidationBridge) GetAsgards() (ttypes.Vaults, error) {
	b.mu.Lock()
	b.asgardCalls++
	b.mu.Unlock()
	return b.asgards, nil
}

func (b *consolidationBridge) GetBlockHeight() (int64, error) {
	return b.blockHeight, nil
}

func (b *consolidationBridge) GetMimir(key string) (int64, error) {
	b.mu.Lock()
	b.mimirCalls++
	b.mu.Unlock()
	if b.mimir == nil {
		return -1, nil
	}
	if value, ok := b.mimir[key]; ok {
		return value, nil
	}
	return -1, nil
}

func (b *consolidationBridge) mimirCallCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.mimirCalls
}

func (b *consolidationBridge) GetConstants() (map[string]int64, error) {
	if b.constants == nil {
		return map[string]int64{
			constants.ChurnInterval.String(): 100,
		}, nil
	}
	return b.constants, nil
}

func (b *consolidationBridge) GetVault(pubkey string) (ttypes.Vault, error) {
	for _, vault := range b.asgards {
		if vault.PubKey.String() == pubkey || vault.PubKeyFrost.String() == pubkey {
			return vault, nil
		}
	}
	return ttypes.Vault{}, errors.New("vault not found")
}

// GetKeysignParty exposes vault membership to consolidation tests through the
// legacy bridge method used before full policy metadata existed.
func (b *consolidationBridge) GetKeysignParty(vaultPubKey common.PubKey, _ int64) (common.PubKeys, error) {
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

// GetKeysignPolicy synthesizes a full policy from membership for consolidation
// tests that only need all vault members to remain eligible.
func (b *consolidationBridge) GetKeysignPolicy(vaultPubKey common.PubKey, blockHeight int64) (thorclient.KeysignPolicy, error) {
	signers, err := b.GetKeysignParty(vaultPubKey, blockHeight)
	if err != nil {
		return thorclient.KeysignPolicy{}, err
	}
	return thorclient.KeysignPolicy{PubKey: vaultPubKey, Signers: signers, RequiredSigners: len(signers), ActiveSignerCount: len(signers)}, nil
}

func (b *consolidationBridge) GetThorchainVersion() (semver.Version, error) {
	return semver.MustParse("1.134.0"), nil
}

type consolidationTSSResponse struct {
	resp tssp.FrostKeySignResponse
	err  error
}

type consolidationTSS struct {
	tssp.Server

	mu       sync.Mutex
	sequence []consolidationTSSResponse
	calls    int
	lastReq  tssp.FrostKeySignRequest
}

func (m *consolidationTSS) FrostKeySign(req tssp.FrostKeySignRequest, _ tssp.FrostSidecarClient) (tssp.FrostKeySignResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.lastReq = req
	if len(m.sequence) == 0 {
		return tssp.FrostKeySignResponse{}, errors.New("no configured tss response")
	}
	idx := m.calls - 1
	if idx >= len(m.sequence) {
		idx = len(m.sequence) - 1
	}
	resp := m.sequence[idx].resp
	if m.sequence[idx].err != nil {
		return resp, m.sequence[idx].err
	}
	if resp.Status == tsscommon.Success {
		msgID, err := tssp.FrostKeySignMsgID(req)
		if err != nil {
			return tssp.FrostKeySignResponse{}, err
		}
		resp.MsgID = msgID
		if len(strings.TrimSpace(resp.TxHash)) != 64 {
			hashChar := string(rune('a' + ((m.calls - 1) % 26)))
			resp.TxHash = strings.Repeat(hashChar, 64)
		}
		if strings.TrimSpace(resp.TxHex) == "" {
			resp.TxHex = "deadbeef"
		}
		if strings.TrimSpace(resp.TxKey) == "" {
			resp.TxKey = strings.Repeat("1", 64)
		}
		if resp.Eventuality.Version == 0 {
			planHash, err := tssp.FrostPlanHash(req, resp.MsgID)
			if err != nil {
				return tssp.FrostKeySignResponse{}, err
			}
			eventuality, err := tssp.BuildFrostEventuality(planHash, resp.TxHash, resp.TxHex, resp.TxKey, resp.FeeAtomic)
			if err != nil {
				return tssp.FrostKeySignResponse{}, err
			}
			resp.Eventuality = eventuality
		}
	}
	return resp, nil
}

func (m *consolidationTSS) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *consolidationTSS) request() tssp.FrostKeySignRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastReq
}

type consolidationSidecar struct {
	mu sync.Mutex

	balanceByVault map[string]sidecar.Balance
	outputsByVault map[string][]sidecar.Output
	mempool        []sidecar.Transfer
	feeRate        uint64
	onOutputs      func()

	broadcastCount int
	lastBroadcast  sidecar.BroadcastReq
}

func (s *consolidationSidecar) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/keys":
		s.mu.Lock()
		vaults := make([]sidecar.VaultKey, 0, len(s.balanceByVault))
		for pubKey := range s.balanceByVault {
			vault, err := common.NewPubKey(pubKey)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			groupKey, err := groupKeyHexFromVaultPubKey(vault)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			vaults = append(vaults, sidecar.VaultKey{VaultPubKey: pubKey, GroupKey: groupKey, HasFrostKeys: true, Signable: true})
		}
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"vaults": vaults})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
		_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(0))
	case strings.HasPrefix(r.URL.Path, "/v1/monero/balance/"):
		vault := strings.TrimPrefix(r.URL.Path, "/v1/monero/balance/")
		s.mu.Lock()
		resp, ok := s.balanceByVault[vault]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(resp)
	case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
		vault := strings.TrimPrefix(r.URL.Path, "/v1/monero/outputs/")
		s.mu.Lock()
		outputs := append([]sidecar.Output(nil), s.outputsByVault[vault]...)
		onOutputs := s.onOutputs
		s.mu.Unlock()
		if onOutputs != nil {
			onOutputs()
		}
		_ = json.NewEncoder(w).Encode(sidecar.Outputs{
			Outputs: outputs,
		})
	case strings.HasPrefix(r.URL.Path, "/v1/monero/transfers"):
		s.mu.Lock()
		outgoing := append([]sidecar.Transfer(nil), s.mempool...)
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(sidecar.Transfers{
			Outgoing: outgoing,
		})
	case r.URL.Path == "/v1/monero/fee-rate":
		s.mu.Lock()
		feeRate := s.feeRate
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(sidecar.FeeRate{
			FeeRatePerWeight: feeRate,
		})
	case r.URL.Path == "/v1/monero/broadcast":
		var req sidecar.BroadcastReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.broadcastCount++
		s.lastBroadcast = req
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(sidecar.BroadcastResp{
			TxHash: "xmr-consolidation-hash",
			TxKey:  req.TxKey,
		})
	default:
		http.NotFound(w, r)
	}
}

func (s *consolidationSidecar) broadcasts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.broadcastCount
}

func newConsolidationClient(
	t *testing.T,
	bridge *consolidationBridge,
	tssServer *consolidationTSS,
	ss *consolidationSidecar,
	localNodePubKey common.PubKey,
	vaultPubKey common.PubKey,
	vaultAddr common.Address,
) *Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(ss.handler))
	t.Cleanup(server.Close)

	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		logger:                  log.Logger,
		cfg:                     config.BifrostChainConfiguration{ChainID: common.XMRChain, XMRConsolidationEnabled: true, XMRConsolidationCheckIntervalBlocks: 10, XMRConsolidationCooldownBlocks: 1, XMRConsolidationMinUnlockedOutputs: 2, XMRConsolidationTargetOutputs: 1, XMRConsolidationMinUnlockedAtomic: 1, XMRConsolidationReserveBps: 50, XMRConsolidationChurnLookaheadBlocks: 50, XMRConsolidationMaxRetries: 3, XMRConsolidationMaxInputs: 8, XMRConsolidationMaxLockBps: 1000},
		chain:                   common.XMRChain,
		thorchainBridge:         bridge,
		localNodePubKey:         localNodePubKey,
		sidecar:                 frostSigner,
		tssServer:               tssServer,
		signerCacheManager:      newTestSignerCacheManager(t),
		signedTxPayloadStore:    newTestSignedTxPayloadStore(t),
		vaultAddressMap:         map[string]common.Address{vaultPubKey.String(): vaultAddr},
		scanner:                 &Scanner{currentBlockHeight: atomic.NewInt64(100), lastFee: atomic.NewUint64(10)},
		vaultLocks:              make(map[string]*sync.Mutex),
		consolidationState:      make(map[string]consolidationVaultState),
		consolidateInProgress:   atomic.NewBool(false),
		lastConsolidationHeight: atomic.NewInt64(0),
	}
	return c
}

func unlockedOutputs(height uint64, n int) []sidecar.Output {
	out := make([]sidecar.Output, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, sidecar.Output{
			TxHash:      strings.Repeat("a", 63) + string(rune('a'+i)),
			OutputIndex: uint32(i),
			Amount:      1_000_000_000_000,
			Height:      height,
			Spent:       false,
		})
	}
	return out
}

func TestLoadConsolidationSettingsCachesMimirReads(t *testing.T) {
	t.Parallel()

	bridge := &consolidationBridge{
		mimir: map[string]int64{
			mimirXMRConsolidationEnabled:             1,
			mimirXMRConsolidationCheckIntervalBlocks: 12,
			mimirXMRConsolidationCooldownBlocks:      3,
			mimirXMRConsolidationMinUnlockedOutputs:  4,
			mimirXMRConsolidationTargetOutputs:       2,
			mimirXMRConsolidationMinUnlockedAtomic:   42,
			mimirXMRConsolidationReserveBps:          75,
			mimirXMRConsolidationChurnLookahead:      8,
			mimirXMRConsolidationMaxRetries:          5,
			mimirXMRConsolidationMaxInputs:           6,
			mimirXMRConsolidationMaxLockBps:          900,
		},
	}
	client := &Client{
		cfg:             config.BifrostChainConfiguration{XMRConsolidationEnabled: true},
		thorchainBridge: bridge,
	}

	first := client.loadConsolidationSettings()
	second := client.loadConsolidationSettings()

	if !first.Enabled || !second.Enabled {
		t.Fatal("expected consolidation settings to remain enabled")
	}
	if first.MaxInputs != 6 || first.MaxLockBps != 900 {
		t.Fatalf("unexpected max inputs/lock settings: %+v", first)
	}
	if bridge.mimirCallCount() != 11 {
		t.Fatalf("expected one mimir fetch round (11 calls), got %d", bridge.mimirCallCount())
	}

	client.consolidationSettingsMu.Lock()
	client.lastConsolidationConfig = time.Now().Add(-2 * constants.ThorchainBlockTime)
	client.consolidationSettingsMu.Unlock()

	_ = client.loadConsolidationSettings()
	if bridge.mimirCallCount() != 22 {
		t.Fatalf("expected cache refresh after ttl expiry, got %d mimir calls", bridge.mimirCallCount())
	}
}

func TestRunConsolidationPassBroadcastsSelfSend(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
		constants: map[string]int64{
			constants.ChurnInterval.String(): 100,
		},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{
				resp: tssp.FrostKeySignResponse{
					TxHash:    "xmr-sign-hash",
					TxHex:     "deadbeef",
					FeeAtomic: 1_000,
					MsgID:     "consolidation-msg-1",
					Status:    tsscommon.Success,
				},
			},
		},
	}
	sc := &consolidationSidecar{
		balanceByVault: map[string]sidecar.Balance{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]sidecar.Output{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sc, localNodePubKey, vaultPubKey, vaultAddr)

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 1 {
		t.Fatalf("expected one FrostKeySign call, got %d", tssServer.callCount())
	}
	if sc.broadcasts() != 1 {
		t.Fatalf("expected one broadcast, got %d", sc.broadcasts())
	}
	req := tssServer.request()
	if req.ToAddress != vaultAddr.String() {
		t.Fatalf("expected self-send address %s, got %s", vaultAddr, req.ToAddress)
	}
	if req.Memo != "consolidate" {
		t.Fatalf("expected consolidate memo, got %s", req.Memo)
	}
	if strings.TrimSpace(req.RequestID) == "" {
		t.Fatal("expected non-empty keysign request_id")
	}
	if req.AmountAtomic == 0 {
		t.Fatal("expected non-zero consolidation amount")
	}
}

func TestEnsureConsolidationStateInitializesZeroValueClient(t *testing.T) {
	t.Parallel()

	client := &Client{}
	client.ensureConsolidationState()
	if client.consolidateInProgress == nil {
		t.Fatal("expected consolidateInProgress to be initialized")
	}
	if client.lastConsolidationHeight == nil {
		t.Fatal("expected lastConsolidationHeight to be initialized")
	}
	if client.lastSolvencyCheckHeight == nil {
		t.Fatal("expected lastSolvencyCheckHeight to be initialized")
	}
	if client.vaultLocks == nil {
		t.Fatal("expected vaultLocks to be initialized")
	}
	if client.consolidationState == nil {
		t.Fatal("expected consolidationState to be initialized")
	}
	lock1 := client.GetVaultLock("")
	lock2 := client.GetVaultLock("")
	if lock1 != lock2 {
		t.Fatal("expected empty vault lock lookups to share the same mutex")
	}
}

func TestRunConsolidationPassUsesSpendableBalanceForAmount(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
		constants: map[string]int64{
			constants.ChurnInterval.String(): 100,
		},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{
				resp: tssp.FrostKeySignResponse{
					TxHash:    "xmr-sign-hash",
					TxHex:     "deadbeef",
					FeeAtomic: 1_000,
					MsgID:     "consolidation-msg-spendable",
					Status:    tsscommon.Success,
				},
			},
		},
	}
	spendableAtomic := uint64(3_000_000_000_000)
	sc := &consolidationSidecar{
		balanceByVault: map[string]sidecar.Balance{
			vaultPubKey.String(): {
				Balance:          5_000_000_000_000,
				UnlockedBalance:  5_000_000_000_000,
				SpendableBalance: uint64Ptr(spendableAtomic),
			},
		},
		outputsByVault: map[string][]sidecar.Output{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sc, localNodePubKey, vaultPubKey, vaultAddr)

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 1 {
		t.Fatalf("expected one FrostKeySign call, got %d", tssServer.callCount())
	}

	req := tssServer.request()
	settings := client.loadConsolidationSettings()
	expectedReserve := initialConsolidationReserve(spendableAtomic, settings.ReserveBps)
	expectedAmount := spendableAtomic - expectedReserve
	expectedBudget := xmrConsolidationMaxLockAtomic(spendableAtomic, settings.MaxLockBps)
	if expectedAmount > expectedBudget {
		expectedAmount = expectedBudget
	}
	if req.AmountAtomic != expectedAmount {
		t.Fatalf("expected AmountAtomic=%d from spendable balance, got %d", expectedAmount, req.AmountAtomic)
	}
	if req.MaxInputs != settings.MaxInputs {
		t.Fatalf("expected consolidation MaxInputs=%d, got %d", settings.MaxInputs, req.MaxInputs)
	}
	if req.MaxSelectedInputValueAtomic != expectedBudget {
		t.Fatalf("expected consolidation MaxSelectedInputValueAtomic=%d, got %d", expectedBudget, req.MaxSelectedInputValueAtomic)
	}
}

func TestRunConsolidationPassSkipsWhenSpendableBalanceBelowThreshold(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	minSpendableAtomic := int64(4_000_000_000_000)
	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
		mimir: map[string]int64{
			mimirXMRConsolidationMinUnlockedAtomic: minSpendableAtomic,
		},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{resp: tssp.FrostKeySignResponse{Status: tsscommon.Success, TxHash: "x", TxHex: "deadbeef", MsgID: "consolidation-msg-threshold"}},
		},
	}
	sc := &consolidationSidecar{
		balanceByVault: map[string]sidecar.Balance{
			vaultPubKey.String(): {
				Balance:          5_000_000_000_000,
				UnlockedBalance:  5_000_000_000_000,
				SpendableBalance: uint64Ptr(3_000_000_000_000),
			},
		},
		outputsByVault: map[string][]sidecar.Output{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sc, localNodePubKey, vaultPubKey, vaultAddr)

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 0 {
		t.Fatalf("expected no FrostKeySign call when spendable balance is below threshold, got %d", tssServer.callCount())
	}
	if sc.broadcasts() != 0 {
		t.Fatalf("expected no broadcast when spendable balance is below threshold, got %d", sc.broadcasts())
	}
}

func TestRunConsolidationPassSkipsWhenMempoolOutgoingExists(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{resp: tssp.FrostKeySignResponse{Status: tsscommon.Success, TxHash: "x", TxHex: "deadbeef", MsgID: "consolidation-msg-skip"}},
		},
	}
	sc := &consolidationSidecar{
		balanceByVault: map[string]sidecar.Balance{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]sidecar.Output{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		mempool: []sidecar.Transfer{
			{VaultPubKey: vaultPubKey.String(), FromAddress: vaultAddr.String()},
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sc, localNodePubKey, vaultPubKey, vaultAddr)

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 0 {
		t.Fatalf("expected no FrostKeySign call, got %d", tssServer.callCount())
	}
	if sc.broadcasts() != 0 {
		t.Fatalf("expected no broadcast, got %d", sc.broadcasts())
	}
}

func TestRunConsolidationPassSkipsRetiringVaultConsolidation(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Status:      ttypes.VaultStatus_RetiringVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{
				resp: tssp.FrostKeySignResponse{
					TxHash:    "xmr-sign-hash",
					TxHex:     "deadbeef",
					FeeAtomic: 1_000,
					MsgID:     "consolidation-msg-retiring",
					Status:    tsscommon.Success,
				},
			},
		},
	}
	sc := &consolidationSidecar{
		balanceByVault: map[string]sidecar.Balance{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]sidecar.Output{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sc, localNodePubKey, vaultPubKey, vaultAddr)

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 0 {
		t.Fatalf("expected no FrostKeySign call for retiring vault, got %d", tssServer.callCount())
	}
}

func TestRunConsolidationPassSkipsNearChurn(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
		constants: map[string]int64{
			constants.ChurnInterval.String(): 60,
		},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{{
			resp: tssp.FrostKeySignResponse{
				TxHash:    "xmr-sign-hash",
				TxHex:     "deadbeef",
				FeeAtomic: 1_000,
				MsgID:     "consolidation-msg-near-churn",
				Status:    tsscommon.Success,
			},
		}},
	}
	sc := &consolidationSidecar{
		balanceByVault: map[string]sidecar.Balance{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]sidecar.Output{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sc, localNodePubKey, vaultPubKey, vaultAddr)

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 0 {
		t.Fatalf("expected no FrostKeySign call near churn, got %d", tssServer.callCount())
	}
}

func TestRunConsolidationPassSkipsWhenNodeInactive(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Standby,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{resp: tssp.FrostKeySignResponse{Status: tsscommon.Success, TxHash: "x", TxHex: "deadbeef", MsgID: "consolidation-msg-inactive"}},
		},
	}
	sc := &consolidationSidecar{
		balanceByVault: map[string]sidecar.Balance{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]sidecar.Output{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sc, localNodePubKey, vaultPubKey, vaultAddr)

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 0 {
		t.Fatalf("expected no FrostKeySign call, got %d", tssServer.callCount())
	}
}

func TestRunConsolidationPassRetriesOnInsufficientFunds(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{
				err: errors.New("insufficient funds while building tx"),
			},
			{
				resp: tssp.FrostKeySignResponse{
					TxHash:    "xmr-sign-hash",
					TxHex:     "deadbeef",
					FeeAtomic: 1_000,
					MsgID:     "consolidation-msg-retry",
					Status:    tsscommon.Success,
				},
			},
		},
	}
	sc := &consolidationSidecar{
		balanceByVault: map[string]sidecar.Balance{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]sidecar.Output{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sc, localNodePubKey, vaultPubKey, vaultAddr)

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 2 {
		t.Fatalf("expected two FrostKeySign calls, got %d", tssServer.callCount())
	}
	if sc.broadcasts() != 1 {
		t.Fatalf("expected one broadcast after retry, got %d", sc.broadcasts())
	}
}

func TestRunConsolidationPassHoldsVaultLockDuringPrechecks(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{
				resp: tssp.FrostKeySignResponse{
					TxHash:    "xmr-sign-hash",
					TxHex:     "deadbeef",
					FeeAtomic: 1_000,
					MsgID:     "consolidation-msg-locking",
					Status:    tsscommon.Success,
				},
			},
		},
	}
	lockObservedUnlocked := atomic.NewBool(false)
	sc := &consolidationSidecar{
		balanceByVault: map[string]sidecar.Balance{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]sidecar.Output{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sc, localNodePubKey, vaultPubKey, vaultAddr)
	sc.onOutputs = func() {
		lock := client.GetVaultLock(vaultPubKey.String())
		if lock.TryLock() {
			lockObservedUnlocked.Store(true)
			lock.Unlock()
		}
	}

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if lockObservedUnlocked.Load() {
		t.Fatalf("expected consolidation prechecks to run while the vault lock is held")
	}
}

func TestRunConsolidationPassBacksOffAcrossPasses(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{err: errors.New("insufficient funds while building tx")},
		},
	}
	sc := &consolidationSidecar{
		balanceByVault: map[string]sidecar.Balance{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]sidecar.Output{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sc, localNodePubKey, vaultPubKey, vaultAddr)
	client.cfg.XMRConsolidationCheckIntervalBlocks = 1
	client.cfg.XMRConsolidationCooldownBlocks = 2
	client.cfg.XMRConsolidationMaxRetries = 1

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("first runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 1 {
		t.Fatalf("expected first pass to attempt signing once, got %d", tssServer.callCount())
	}

	bridge.blockHeight = 22
	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("second runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 2 {
		t.Fatalf("expected second pass to retry after base cooldown, got %d calls", tssServer.callCount())
	}

	bridge.blockHeight = 24
	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("third runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 2 {
		t.Fatalf("expected third pass to back off due to scaled cooldown, got %d calls", tssServer.callCount())
	}
}

func TestRunConsolidationPassStopsOnNonRetryableError(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{err: errors.New("invalid address supplied to keysign request")},
			{resp: tssp.FrostKeySignResponse{Status: tsscommon.Success, TxHash: "x", TxHex: "deadbeef", MsgID: "should-not-run"}},
		},
	}
	sc := &consolidationSidecar{
		balanceByVault: map[string]sidecar.Balance{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]sidecar.Output{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sc, localNodePubKey, vaultPubKey, vaultAddr)
	client.cfg.XMRConsolidationCheckIntervalBlocks = 1
	client.cfg.XMRConsolidationMaxRetries = 3

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 1 {
		t.Fatalf("expected non-retryable failure to stop after one attempt, got %d", tssServer.callCount())
	}
	if sc.broadcasts() != 0 {
		t.Fatalf("expected no broadcast on non-retryable signing failure, got %d", sc.broadcasts())
	}
}

func TestBumpConsolidationReserveSaturatesOnOverflow(t *testing.T) {
	t.Parallel()

	currentReserve := ^uint64(0) - defaultXMRConsolidationMinReserveAtomic + 1
	nextReserve := bumpConsolidationReserve(currentReserve, defaultXMRConsolidationReserveBps)
	if nextReserve != ^uint64(0) {
		t.Fatalf("expected reserve to saturate at MaxUint64, got %d", nextReserve)
	}
}

func TestInitialConsolidationReserveHandlesMultiplicationOverflow(t *testing.T) {
	t.Parallel()

	availableAtomic := ^uint64(0)
	reserveBps := defaultXMRConsolidationReserveBps

	expected := new(big.Int).SetUint64(availableAtomic)
	expected.Mul(expected, big.NewInt(reserveBps))
	expected.Quo(expected, big.NewInt(10_000))
	if !expected.IsUint64() {
		t.Fatal("expected scaled reserve to fit in uint64 after division")
	}

	got := initialConsolidationReserve(availableAtomic, reserveBps)
	if got != expected.Uint64() {
		t.Fatalf("expected reserve %d, got %d", expected.Uint64(), got)
	}
}

func TestBumpConsolidationReserveHandlesScaledStepOverflow(t *testing.T) {
	t.Parallel()

	currentReserve := uint64(9_500_000_000_000_000_000)
	nextReserve := bumpConsolidationReserve(currentReserve, 10_000)
	if nextReserve != ^uint64(0) {
		t.Fatalf("expected scaled reserve bump to saturate at MaxUint64, got %d", nextReserve)
	}
}

func TestScaledConsolidationCooldownCapsOnOverflow(t *testing.T) {
	t.Parallel()

	cooldown := scaledConsolidationCooldown(math.MaxInt64/2+1, 2)
	if cooldown != math.MaxInt64 {
		t.Fatalf("expected cooldown to saturate at MaxInt64, got %d", cooldown)
	}
}
