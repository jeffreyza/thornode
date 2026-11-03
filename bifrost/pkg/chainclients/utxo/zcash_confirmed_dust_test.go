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
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcutil"
	. "gopkg.in/check.v1"

	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	utxorpcc "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type zecConfirmedDustBridge struct {
	thorclient.ThorchainBridge
	pairs []thorclient.PubKeyContractAddressPair
	err   error
	calls int
}

func (b *zecConfirmedDustBridge) GetAsgardPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	b.calls++
	return b.pairs, b.err
}

type zecConfirmedDustView struct {
	inputs       []zecInputSelectionCandidate
	transactions map[string]*btcjson.TxRawResult
	failures     map[string]bool
	calls        map[string]int
	mu           sync.Mutex
}

func (s *ZcashSignerSuite) zecConfirmedDustRPC(c *C, fixture *zecExpiryFixture) (*zecConfirmedDustView, func()) {
	script, err := s.client.getSourceScript(fixture.tx)
	c.Assert(err, IsNil)
	address, err := fixture.tx.VaultPubKey.GetAddress(common.ZECChain)
	c.Assert(err, IsNil)
	reference := fixture.bridge.observedHeight
	fixture.localHeight.Store(reference + 30)
	large := zecInputSelectionCandidate{TxID: strings.Repeat("aa", 32), Address: address.String(), Script: hex.EncodeToString(script), Satoshis: 1000000, Height: reference - 10}
	dust := large
	dust.TxID, dust.Satoshis, dust.Height = strings.Repeat("bb", 32), 9000, reference+3
	previousID := strings.Repeat("cc", 32)
	view := &zecConfirmedDustView{
		inputs: []zecInputSelectionCandidate{dust, large},
		transactions: map[string]*btcjson.TxRawResult{
			dust.TxID: {
				Txid: dust.TxID,
				Vin:  []btcjson.Vin{{Txid: previousID, Vout: 1}},
				Vout: []btcjson.Vout{{N: 0, Value: 0.00009, ScriptPubKey: btcjson.ScriptPubKeyResult{Hex: dust.Script}}},
			},
			previousID: {
				Txid: previousID,
				Vout: []btcjson.Vout{{N: 0}, {N: 1, Value: 1, ScriptPubKey: btcjson.ScriptPubKeyResult{Hex: dust.Script}}},
			},
		},
		failures: make(map[string]bool), calls: make(map[string]int),
	}
	target, err := url.Parse(fixture.rpcServer.URL)
	c.Assert(err, IsNil)
	proxy := httputil.NewSingleHostReverseProxy(target)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		c.Assert(readErr, IsNil)
		var request struct {
			Method string            `json:"method"`
			ID     json.RawMessage   `json:"id"`
			Params []json.RawMessage `json:"params"`
		}
		c.Assert(json.Unmarshal(body, &request), IsNil)
		var result, rpcErr any
		switch request.Method {
		case "getaddressutxos":
			result = view.inputs
		case "getrawtransaction":
			var id string
			c.Assert(json.Unmarshal(request.Params[0], &id), IsNil)
			view.mu.Lock()
			view.calls[id]++
			if view.failures[id] {
				rpcErr = map[string]any{"code": -1, "message": "dust history unavailable"}
			} else {
				result = view.transactions[id]
			}
			view.mu.Unlock()
		default:
			r.Body = io.NopCloser(bytes.NewReader(body))
			proxy.ServeHTTP(w, r)
			return
		}
		c.Assert(json.NewEncoder(w).Encode(map[string]any{"id": request.ID, "result": result, "error": rpcErr}), IsNil)
	}))
	originalRPC := s.client.rpc
	s.client.rpc, err = utxorpcc.NewClient(server.Listener.Addr().String(), s.cfg.UserName, s.cfg.Password, 0, time.Second, common.ZECChain, s.client.log)
	c.Assert(err, IsNil)
	return view, func() { s.client.rpc = originalRPC; server.Close() }
}

func (s *ZcashSignerSuite) TestZECConfirmedOwnDustFundsFullBalanceAndRetiredRetry(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	view, restore := s.zecConfirmedDustRPC(c, fixture)
	defer restore()
	reference := fixture.bridge.observedHeight
	membership := &zecConfirmedDustBridge{ThorchainBridge: fixture.bridge, err: errors.New("own change must not query membership")}
	s.client.bridge = membership
	selected, err := s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, btcutil.Amount(1009000), reference, false)
	c.Assert(err, IsNil)
	c.Assert(zecInputSelectionKeys(selected), DeepEquals, []string{formatUtxoKey(view.inputs[1].TxID, 0), formatUtxoKey(view.inputs[0].TxID, 0)})
	s.client.bridge = &zecFeeBridge{ThorchainBridge: membership, maxUTXOs: 2}
	consolidated, err := s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, btcutil.Amount(0), reference, true)
	c.Assert(err, IsNil)
	c.Assert(consolidated, DeepEquals, selected)
	s.client.bridge = membership
	tx := fixture.tx
	tx.Coins = common.NewCoins(common.NewCoin(common.ZECAsset, cosmos.NewUint(909000)))
	_, checkpoint, _, err := s.client.SignTx(tx, 1237)
	c.Assert(err, ErrorMatches, "(?s).*injected signing failure.*")
	c.Assert(len(checkpoint) > 0, Equals, true)
	c.Assert(fixture.signer.digests, HasLen, 2)
	firstDigests := []string{hex.EncodeToString(fixture.signer.digests[0]), hex.EncodeToString(fixture.signer.digests[1])}
	creatingCalls := view.calls[view.inputs[0].TxID]
	originalCache := s.client.asgardCache.Load()
	defer func() { s.client.asgardCache.Store(originalCache) }()
	s.client.asgardCache.Store(&sharedutxo.AsgardCache{FetchedAt: time.Now()})
	tx.Checkpoint = checkpoint
	_, saved, _, err := s.client.SignTx(tx, 1237)
	c.Assert(err, ErrorMatches, "(?s).*injected signing failure.*")
	c.Assert(saved, DeepEquals, checkpoint)
	c.Assert(view.calls[view.inputs[0].TxID], Equals, creatingCalls)
	c.Assert(fixture.signer.digests, HasLen, 4)
	secondDigests := []string{hex.EncodeToString(fixture.signer.digests[2]), hex.EncodeToString(fixture.signer.digests[3])}
	sort.Strings(firstDigests)
	sort.Strings(secondDigests)
	c.Assert(secondDigests, DeepEquals, firstDigests)
	selected, err = s.client.getZecUtxoToSpend(tx.VaultPubKey, btcutil.Amount(1009000), reference, false)
	c.Assert(err, IsNil)
	c.Assert(selected, HasLen, 2)
	c.Assert(membership.calls, Equals, 0)
}

func (s *ZcashSignerSuite) TestZECConfirmedDustExcludesExternalAndUnconfirmed(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	view, restore := s.zecConfirmedDustRPC(c, fixture)
	defer restore()
	reference := fixture.bridge.observedHeight
	membership := &zecConfirmedDustBridge{ThorchainBridge: fixture.bridge}
	s.client.bridge = membership
	dustID, previousID := view.inputs[0].TxID, strings.Repeat("cc", 32)
	creating := view.transactions[dustID]
	validInputs := creating.Vin
	for _, inputs := range [][]btcjson.Vin{nil, {{Coinbase: "0101"}}} {
		creating.Vin = inputs
		selected, err := s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, btcutil.Amount(1009000), reference, false)
		c.Assert(err, NotNil)
		c.Assert(selected, HasLen, 0)
		c.Assert(view.calls[previousID], Equals, 0)
	}
	creating.Vin = validInputs
	view.transactions[previousID].Vout[1].ScriptPubKey.Hex = "76a914" + strings.Repeat("11", 20) + "88ac"
	selected, err := s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, btcutil.Amount(1009000), reference, false)
	c.Assert(err, NotNil)
	c.Assert(selected, HasLen, 0)
	c.Assert(view.calls[previousID], Equals, 1)
	c.Assert(membership.calls, Equals, 1)
	view.inputs[0].Height = 0
	view.calls = make(map[string]int)
	selected, err = s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, btcutil.Amount(1009000), reference, false)
	c.Assert(err, NotNil)
	c.Assert(selected, HasLen, 0)
	c.Assert(view.calls[dustID], Equals, 0)
}

func (s *ZcashSignerSuite) TestZECConfirmedCrossVaultDustUsesFreshMembershipOnce(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	view, restore := s.zecConfirmedDustRPC(c, fixture)
	defer restore()
	reference := fixture.bridge.observedHeight
	foreignPrivate, _ := btcec.PrivKeyFromBytes(btcec.S256(), bytes.Repeat([]byte{2}, 32))
	foreignKey, err := bech32AccountPubKey(foreignPrivate)
	c.Assert(err, IsNil)
	foreignTx := fixture.tx
	foreignTx.VaultPubKey = foreignKey
	foreignScript, err := s.client.getSourceScript(foreignTx)
	c.Assert(err, IsNil)
	view.transactions[strings.Repeat("cc", 32)].Vout[1].ScriptPubKey.Hex = hex.EncodeToString(foreignScript)
	membership := &zecConfirmedDustBridge{ThorchainBridge: fixture.bridge, pairs: []thorclient.PubKeyContractAddressPair{
		{PubKey: types2.GetRandomEd25519PubKey(), Algo: common.SigningAlgoEd25519},
		{PubKey: foreignKey, Algo: common.SigningAlgoSecp256k1},
	}}
	s.client.bridge = membership
	originalCache := s.client.asgardCache.Load()
	defer func() { s.client.asgardCache.Store(originalCache) }()
	s.client.asgardCache.Store(&sharedutxo.AsgardCache{FetchedAt: time.Now()})
	dust := view.inputs[0]
	second := dust
	second.Index = 1
	view.inputs = append(view.inputs, second)
	creating := view.transactions[dust.TxID]
	creating.Vout = append(creating.Vout, creating.Vout[0])
	creating.Vout[1].N = 1
	selected, err := s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, btcutil.Amount(1018000), reference, false)
	c.Assert(err, IsNil)
	c.Assert(selected, HasLen, 3)
	c.Assert(membership.calls, Equals, 1)
	c.Assert(view.calls[dust.TxID], Equals, 1)

	// A failed source-membership query must not substitute a later input.
	alternate := view.inputs[1]
	alternate.TxID, alternate.Height = strings.Repeat("dd", 32), reference+4
	view.inputs = append(view.inputs, alternate)
	membership.err = errors.New("membership unavailable")
	tx := fixture.tx
	tx.Coins = common.NewCoins(common.NewCoin(common.ZECAsset, cosmos.NewUint(909000)))
	payload, checkpoint, _, err := s.client.SignTx(tx, 1237)
	c.Assert(err, ErrorMatches, "(?s).*membership unavailable.*")
	c.Assert(payload, HasLen, 0)
	c.Assert(checkpoint, HasLen, 0)
	c.Assert(fixture.signer.digests, HasLen, 0)
	c.Assert(membership.calls, Equals, 2)
}

func (s *ZcashSignerSuite) TestZECConfirmedDustProofFailuresDeferBeforeTSS(c *C) {
	for _, failure := range []string{"creating-rpc", "creating-id", "candidate-id", "creating-output", "creating-script", "previous-rpc", "previous-id", "previous-output", "previous-script", "previous-push", "input-id"} {
		fixture := s.newZecExpiryFixture(c)
		view, restore := s.zecConfirmedDustRPC(c, fixture)
		dustID, previousID := view.inputs[0].TxID, strings.Repeat("cc", 32)
		switch failure {
		case "creating-rpc":
			view.failures[dustID] = true
		case "creating-id":
			view.transactions[dustID].Txid = strings.Repeat("dd", 32)
		case "candidate-id":
			view.inputs[0].TxID = "bb"
		case "creating-output":
			view.transactions[dustID].Vout[0].N = 1
		case "creating-script":
			view.transactions[dustID].Vout[0].ScriptPubKey.Hex = "zz"
		case "previous-rpc":
			view.failures[previousID] = true
		case "previous-id":
			view.transactions[previousID].Txid = strings.Repeat("dd", 32)
		case "previous-output":
			view.transactions[previousID].Vout = nil
		case "previous-script":
			view.transactions[previousID].Vout[1].ScriptPubKey.Hex = "zz"
		case "previous-push":
			view.transactions[previousID].Vout[1].ScriptPubKey.Hex = "4c"
		case "input-id":
			view.transactions[dustID].Vin[0].Txid = "cc"
		}
		tx := fixture.tx
		tx.Coins = common.NewCoins(common.NewCoin(common.ZECAsset, cosmos.NewUint(909000)))
		payload, checkpoint, _, err := s.client.SignTx(tx, 1237)
		restore()
		fixture.rpcServer.Close()
		c.Assert(err, NotNil, Commentf("proof failure %s", failure))
		c.Assert(payload, HasLen, 0)
		c.Assert(checkpoint, HasLen, 0)
		c.Assert(fixture.signer.digests, HasLen, 0)
	}
}

func (s *ZcashSignerSuite) TestZECConfirmedDustProofCacheAndFundedPrefix(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	view, restore := s.zecConfirmedDustRPC(c, fixture)
	defer restore()
	reference := fixture.bridge.observedHeight
	membership := &zecConfirmedDustBridge{ThorchainBridge: fixture.bridge, err: errors.New("irrelevant membership unavailable")}
	s.client.bridge = membership
	dust := view.inputs[0]
	view.failures[dust.TxID] = true
	selected, err := s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, btcutil.Amount(1000000), reference, false)
	c.Assert(err, IsNil)
	c.Assert(selected, HasLen, 1)
	c.Assert(view.calls, HasLen, 0)
	delete(view.failures, dust.TxID)
	second := dust
	second.Index = 1
	view.inputs = append(view.inputs, second)
	creating := view.transactions[dust.TxID]
	creating.Vout = append(creating.Vout, creating.Vout[0])
	creating.Vout[1].N = 1
	selected, err = s.client.getZecUtxoToSpend(fixture.tx.VaultPubKey, btcutil.Amount(1018000), reference, false)
	c.Assert(err, IsNil)
	c.Assert(selected, HasLen, 3)
	c.Assert(view.calls[dust.TxID], Equals, 1)
	c.Assert(view.calls[strings.Repeat("cc", 32)], Equals, 1)
	c.Assert(membership.calls, Equals, 0)
}
