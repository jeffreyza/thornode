package utxo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	. "gopkg.in/check.v1"

	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	utxorpcc "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// These tests reuse the expiry fixture's real RPC adapter and digest recorder.
// The override changes raw UTXO creation heights, not derived confirmations.
type zecInputSelectionCandidate struct {
	TxID     string `json:"txid"`
	Index    uint32 `json:"outputIndex"`
	Address  string `json:"address"`
	Script   string `json:"script"`
	Satoshis uint64 `json:"satoshis"`
	Height   int64  `json:"height"`
}

func (s *ZcashSignerSuite) zecInputSelectionCandidates(c *C, fixture *zecExpiryFixture) (func(string, int64, uint32, uint64) zecInputSelectionCandidate, *atomic.Value, func()) {
	script, err := s.client.getSourceScript(fixture.tx)
	c.Assert(err, IsNil)
	address, err := fixture.tx.VaultPubKey.GetAddress(common.ZECChain)
	c.Assert(err, IsNil)
	candidate := func(id string, height int64, index uint32, amount uint64) zecInputSelectionCandidate {
		return zecInputSelectionCandidate{TxID: id, Index: index, Address: address.String(), Script: hex.EncodeToString(script), Satoshis: amount, Height: height}
	}
	view := &atomic.Value{}
	view.Store([]zecInputSelectionCandidate{})
	target, err := url.Parse(fixture.rpcServer.URL)
	c.Assert(err, IsNil)
	proxy := httputil.NewSingleHostReverseProxy(target)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		c.Assert(readErr, IsNil)
		var request struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		c.Assert(json.Unmarshal(body, &request), IsNil)
		if request.Method == "getaddressutxos" {
			c.Assert(json.NewEncoder(w).Encode(map[string]any{"id": request.ID, "result": view.Load(), "error": nil}), IsNil)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		proxy.ServeHTTP(w, r)
	}))
	originalRPC := s.client.rpc
	s.client.rpc, err = utxorpcc.NewClient(server.Listener.Addr().String(), s.cfg.UserName, s.cfg.Password, 0, time.Second, common.ZECChain, s.client.log)
	c.Assert(err, IsNil)
	return candidate, view, func() { s.client.rpc = originalRPC; server.Close() }
}

func zecInputSelectionKeys(inputs []btcjson.ListUnspentResult) []string {
	keys := make([]string, len(inputs))
	for i, input := range inputs {
		keys[i] = formatUtxoKey(strings.ToLower(input.TxID), input.Vout)
	}
	return keys
}

// Later confirmed deposits cannot expand a common prefix that already covers
// the payment; this does not claim convergence when a required input changes.
func (s *ZcashSignerSuite) TestZECConfirmedInputsIgnoreLaterDepositsAndLocalTips(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	makeCandidate, view, restore := s.zecInputSelectionCandidates(c, fixture)
	defer restore()
	reference := fixture.bridge.observedHeight
	old := []zecInputSelectionCandidate{
		makeCandidate(strings.Repeat("aa", 32), reference-100, 0, 2000000),
		makeCandidate(strings.Repeat("bb", 32), reference-10, 0, 2000000),
		makeCandidate(strings.Repeat("cc", 32), reference, 0, 2000000),
	}
	later := []zecInputSelectionCandidate{
		makeCandidate(strings.Repeat("dd", 32), reference+1, 0, 2000000),
		makeCandidate(strings.Repeat("ee", 32), reference+14, 0, 2000000),
		makeCandidate(strings.Repeat("ff", 32), reference+16, 0, 2000000),
	}
	expected := []string{formatUtxoKey(old[0].TxID, 0), formatUtxoKey(old[1].TxID, 0), formatUtxoKey(old[2].TxID, 0)}
	for _, count := range []int{0, 1, 3} {
		candidates := append(append([]zecInputSelectionCandidate(nil), old...), later[:count]...)
		for i, j := 0, len(candidates)-1; i < j; i, j = i+1, j-1 {
			candidates[i], candidates[j] = candidates[j], candidates[i]
		}
		view.Store(candidates)
		fixture.localHeight.Store(reference + 17 + int64(count))
		selected, err := s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, btcutil.Amount(5000000), reference, false)
		c.Assert(err, IsNil)
		c.Assert(zecInputSelectionKeys(selected), DeepEquals, expected)
	}
}

func (s *ZcashSignerSuite) TestZECConfirmedInputsOrderAllOutputsAndVetoSpentWinner(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	makeCandidate, view, restore := s.zecInputSelectionCandidates(c, fixture)
	defer restore()
	reference := fixture.bridge.observedHeight
	first := makeCandidate(strings.Repeat("aa", 32), reference-10, 1, 2000000)
	second := makeCandidate(strings.Repeat("AA", 32), reference-10, 3, 2000000)
	alternate := makeCandidate(strings.Repeat("bb", 32), reference-10, 0, 2000000)
	expected := []string{formatUtxoKey(first.TxID, 1), formatUtxoKey(strings.ToLower(second.TxID), 3)}
	for _, candidates := range [][]zecInputSelectionCandidate{{alternate, second, first}, {second, first, alternate}} {
		view.Store(candidates)
		selected, err := s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, btcutil.Amount(3000000), reference, false)
		c.Assert(err, IsNil)
		c.Assert(zecInputSelectionKeys(selected), DeepEquals, expected)
		for _, input := range selected {
			c.Assert(input.TxID, Equals, strings.ToLower(input.TxID))
		}
	}
	// A spent marker outside the shared plan must not alter it.
	c.Assert(s.client.temporalStorage.SetSpentUtxos([]string{formatUtxoKey(alternate.TxID, 0)}, reference), IsNil)
	selected, err := s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, btcutil.Amount(3000000), reference, false)
	c.Assert(err, IsNil)
	c.Assert(zecInputSelectionKeys(selected), DeepEquals, expected)
	// A spent shared winner must defer, not silently substitute another input.
	c.Assert(s.client.temporalStorage.SetSpentUtxos([]string{formatUtxoKey(first.TxID, 1)}, reference), IsNil)
	selected, err = s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, btcutil.Amount(3000000), reference, false)
	c.Assert(err, NotNil)
	c.Assert(selected, HasLen, 0)
}

func (s *ZcashSignerSuite) TestZECConfirmedInputsExcludeUnconfirmedSelfChange(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	makeCandidate, view, restore := s.zecInputSelectionCandidates(c, fixture)
	defer restore()
	reference := fixture.bridge.observedHeight
	confirmed := makeCandidate(strings.Repeat("bb", 32), reference-10, 0, 2000000)
	local := makeCandidate(strings.Repeat("aa", 32), 0, 0, 2000000)
	meta := sharedutxo.NewBlockMeta("", reference, "")
	meta.AddSelfTransaction(local.TxID)
	c.Assert(s.client.temporalStorage.SaveBlockMeta(reference, meta), IsNil)
	for _, height := range []int64{0, -1, fixture.localHeight.Load() + 1} {
		local.Height = height
		view.Store([]zecInputSelectionCandidate{local, confirmed})
		selected, err := s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, btcutil.Amount(1000000), reference, false)
		c.Assert(err, IsNil)
		c.Assert(zecInputSelectionKeys(selected), DeepEquals, []string{formatUtxoKey(confirmed.TxID, 0)})
	}
}

func (s *ZcashSignerSuite) TestZECConfirmedInputsFundPaymentWithChangeAfterReference(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	makeCandidate, view, restore := s.zecInputSelectionCandidates(c, fixture)
	defer restore()
	reference := fixture.bridge.observedHeight
	old := makeCandidate(strings.Repeat("aa", 32), reference-10, 0, 878237)
	change := makeCandidate(strings.Repeat("bb", 32), reference+3, 0, 7880422081)
	fixture.localHeight.Store(reference + 31)
	view.Store([]zecInputSelectionCandidate{change, old})
	selected, err := s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, btcutil.Amount(38428362), reference, false)
	c.Assert(err, IsNil)
	c.Assert(zecInputSelectionKeys(selected), DeepEquals, []string{formatUtxoKey(old.TxID, 0), formatUtxoKey(change.TxID, 0)})

	// The current tip confirms the change; the old seed reference is not a
	// historical input cutoff. Its pinned expiry must remain unchanged.
	_, checkpointBytes, _, err := s.client.SignTx(fixture.tx, 1237)
	c.Assert(err, ErrorMatches, "(?s).*injected signing failure.*")
	var checkpoint sharedutxo.SignCheckpoint
	c.Assert(json.Unmarshal(checkpointBytes, &checkpoint), IsNil)
	c.Assert(checkpoint.IndividualAmounts, HasLen, 2)
	c.Assert(checkpoint.ZecExpiryHeight, Equals, uint32(reference+int64(ZecTxExpiry)))
	c.Assert(fixture.signer.digests, HasLen, 2)
	firstDigests := []string{hex.EncodeToString(fixture.signer.digests[0]), hex.EncodeToString(fixture.signer.digests[1])}
	fixture.localHeight.Store(reference + 32)
	_, repeated, _, err := s.client.SignTx(fixture.tx, 1237)
	c.Assert(err, ErrorMatches, "(?s).*injected signing failure.*")
	c.Assert(repeated, DeepEquals, checkpointBytes)
	c.Assert(fixture.signer.digests, HasLen, 4)
	secondDigests := []string{hex.EncodeToString(fixture.signer.digests[2]), hex.EncodeToString(fixture.signer.digests[3])}
	sort.Strings(firstDigests)
	sort.Strings(secondDigests)
	c.Assert(secondDigests, DeepEquals, firstDigests)
}

func (s *ZcashSignerSuite) TestZECConfirmedPaymentStopsBeforeConsolidationTarget(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	makeCandidate, view, restore := s.zecInputSelectionCandidates(c, fixture)
	defer restore()
	reference := fixture.bridge.observedHeight
	s.client.bridge = &zecFeeBridge{ThorchainBridge: fixture.bridge, maxUTXOs: 3}
	candidates := []zecInputSelectionCandidate{
		makeCandidate(strings.Repeat("aa", 32), reference-30, 0, 2000000),
		makeCandidate(strings.Repeat("bb", 32), reference-20, 0, 2000000),
		makeCandidate(strings.Repeat("cc", 32), reference-10, 0, 2000000),
		makeCandidate(strings.Repeat("dd", 32), reference, 0, 2000000),
	}
	view.Store(candidates)
	payment, err := s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, btcutil.Amount(1000000), reference, false)
	c.Assert(err, IsNil)
	c.Assert(payment, HasLen, 1)
	consolidation, err := s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, btcutil.Amount(0), reference, true)
	c.Assert(err, IsNil)
	c.Assert(consolidation, HasLen, 3)
	c.Assert(zecInputSelectionKeys(payment), DeepEquals, zecInputSelectionKeys(consolidation[:1]))
}

func (s *ZcashSignerSuite) TestZECConfirmedInputPlanAndCheckpointDigestsSurviveNewDeposits(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	makeCandidate, view, restore := s.zecInputSelectionCandidates(c, fixture)
	defer restore()
	reference := fixture.bridge.observedHeight
	first := makeCandidate(strings.Repeat("aa", 32), reference-100, 0, 150000000)
	view.Store([]zecInputSelectionCandidate{first})
	_, checkpoint1, _, err := s.client.SignTx(fixture.tx, 1237)
	c.Assert(err, ErrorMatches, "(?s).*injected signing failure.*")
	c.Assert(len(checkpoint1) > 0, Equals, true)
	view.Store([]zecInputSelectionCandidate{
		makeCandidate(strings.Repeat("bb", 32), reference+1, 0, 150000000),
		makeCandidate(strings.Repeat("cc", 32), reference+16, 0, 150000000),
		first,
	})
	fixture.localHeight.Store(reference + 20)
	_, checkpoint2, _, err := s.client.SignTx(fixture.tx, 1237)
	c.Assert(err, ErrorMatches, "(?s).*injected signing failure.*")
	c.Assert(checkpoint2, DeepEquals, checkpoint1)
	c.Assert(fixture.signer.digests, HasLen, 2)
	c.Assert(fixture.signer.digests[1], DeepEquals, fixture.signer.digests[0])

	// A fresh signer can replay the old checkpoint even when the historical
	// reference query is no longer available and the current input view changed.
	restartedSigner := &zecDigestSigner{privateKey: fixture.signer.privateKey, fail: true}
	restarted, err := NewClient(s.keys, s.client.cfg, nil, s.bridge, s.m)
	c.Assert(err, IsNil)
	restarted.rpc = s.client.rpc
	restarted.bridge = fixture.bridge
	restarted.tssKeySigner = restartedSigner
	restarted.temporalStorage = s.client.temporalStorage
	restarted.signerCacheManager = s.client.signerCacheManager
	fixture.bridge.err = errors.New("historical state pruned")
	tx := fixture.tx
	tx.Checkpoint = checkpoint1
	_, retried, _, err := restarted.SignTx(tx, 1237)
	c.Assert(err, ErrorMatches, "(?s).*injected signing failure.*")
	c.Assert(retried, DeepEquals, checkpoint1)
	c.Assert(restartedSigner.digests, HasLen, 1)
	c.Assert(restartedSigner.digests[0], DeepEquals, fixture.signer.digests[0])
	c.Assert(fixture.bridge.requestedHeights, DeepEquals, []int64{1237, 1237})
}

func (s *ZcashSignerSuite) TestZECConfirmedInputTargetIncludesGasAndExtendedMemoOutputs(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	makeCandidate, view, restore := s.zecInputSelectionCandidates(c, fixture)
	defer restore()
	reference := fixture.bridge.observedHeight
	tx := fixture.tx
	// The valid 103-byte memo needs two funded continuation outputs. Its
	// six ZIP-317 output actions require 30,000 zatoshis with two inputs.
	tx.Memo = "=:ETH.ETH:0x" + strings.Repeat("1", 40) + ":" + strings.Repeat("1", 50)
	view.Store([]zecInputSelectionCandidate{
		makeCandidate(strings.Repeat("aa", 32), reference-20, 0, 100030000),
		makeCandidate(strings.Repeat("bb", 32), reference-10, 0, 1000000),
	})
	for _, maxGas := range []common.Gas{nil, {common.NewCoin(common.ZECAsset, cosmos.NewUint(30000))}} {
		tx.MaxGas = maxGas
		_, checkpointBytes, _, err := s.client.SignTx(tx, 1237)
		c.Assert(err, ErrorMatches, "(?s).*injected signing failure.*")
		var checkpoint sharedutxo.SignCheckpoint
		c.Assert(json.Unmarshal(checkpointBytes, &checkpoint), IsNil)
		unsigned := &wire.MsgTx{}
		c.Assert(unsigned.Deserialize(bytes.NewReader(checkpoint.UnsignedTx)), IsNil)
		c.Assert(unsigned.TxIn, HasLen, 2)
		c.Assert(len(unsigned.TxOut) > 3, Equals, true)
		c.Assert(unsigned.TxOut[0].Value, Equals, int64(100000000))
		var inputs, outputs int64
		for _, amount := range checkpoint.IndividualAmounts {
			inputs += amount
		}
		for _, output := range unsigned.TxOut {
			outputs += output.Value
		}
		c.Assert(inputs-outputs, Equals, int64(30000))
	}
}

func (s *ZcashSignerSuite) TestZECEmptyConsolidationProbeReturnsNoInputs(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	makeCandidate, view, restore := s.zecInputSelectionCandidates(c, fixture)
	defer restore()
	reference := fixture.bridge.observedHeight
	for _, candidates := range [][]zecInputSelectionCandidate{
		{},
		{makeCandidate(strings.Repeat("aa", 32), 0, 0, 200000000)},
	} {
		view.Store(candidates)
		selected, err := s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, 0, reference, true)
		c.Assert(err, IsNil)
		c.Assert(selected, HasLen, 0)
		selected, err = s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, btcutil.Amount(100000000), reference, false)
		c.Assert(err, ErrorMatches, ".*insufficient confirmed ZEC inputs.*")
		c.Assert(selected, HasLen, 0)
		c.Assert(fixture.signer.digests, HasLen, 0)
	}
}
