package common

import (
	"encoding/hex"
	"encoding/json"
	"math/big"

	"github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/cosmos/cosmos-sdk/crypto/codec"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type CoverageSuite struct{}

var _ = Suite(&CoverageSuite{})

// getTestPubKey generates a PubKey from test data for testing
func getTestPubKey(c *C) PubKey {
	privB, _ := hex.DecodeString("ef235aacf90d9f4aadd8c92e4b2562e1d9eb97f0df9ba3b508258739cb013db2")
	priv := secp256k1.PrivKey(privB)
	pubKey := priv.PubKey()
	tmp, err := codec.FromCmtPubKeyInterface(pubKey)
	c.Assert(err, IsNil)
	bech32, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, tmp)
	c.Assert(err, IsNil)
	pk, err := NewPubKey(bech32)
	c.Assert(err, IsNil)
	return pk
}

// getTestPubKey2 generates a second different PubKey for testing
func getTestPubKey2(c *C) PubKey {
	privB, _ := hex.DecodeString("289c2857d4598e37fb9647507e47a309d6133539bf21a8b9cb6df88fd5232032")
	priv := secp256k1.PrivKey(privB)
	pubKey := priv.PubKey()
	tmp, err := codec.FromCmtPubKeyInterface(pubKey)
	c.Assert(err, IsNil)
	bech32, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, tmp)
	c.Assert(err, IsNil)
	pk, err := NewPubKey(bech32)
	c.Assert(err, IsNil)
	return pk
}

// --- ObservedTx tests ---

func (s *CoverageSuite) TestObservedTxIsValid(c *C) {
	otx := &ObservedTx{}
	c.Assert(otx.IsValid(), Equals, true)

	var nilOtx *ObservedTx
	c.Assert(nilOtx.IsValid(), Equals, false)
}

func (s *CoverageSuite) TestObservedTxGetSignablePayload(c *C) {
	tx := Tx{
		ID:          BlankTxID,
		Chain:       BTCChain,
		FromAddress: "from",
		ToAddress:   "to",
		Coins:       Coins{NewCoin(BTCAsset, cosmos.NewUint(100))},
		Gas:         Gas{NewCoin(BTCAsset, cosmos.NewUint(10))},
		Memo:        "test",
	}
	otx := &ObservedTx{Tx: tx}
	payload, err := otx.GetSignablePayloadWithInbound(false)
	c.Assert(err, IsNil)
	c.Assert(len(payload) > 0, Equals, true)
}

func (s *CoverageSuite) TestNewObservedTx(c *C) {
	tx := Tx{
		ID:          BlankTxID,
		Chain:       BTCChain,
		FromAddress: "from",
		ToAddress:   "to",
		Coins:       Coins{NewCoin(BTCAsset, cosmos.NewUint(100))},
		Gas:         Gas{NewCoin(BTCAsset, cosmos.NewUint(10))},
		Memo:        "test",
	}
	pk := getTestPubKey(c)
	otx := NewObservedTx(tx, 100, pk, 200)
	c.Assert(otx.BlockHeight, Equals, int64(100))
	c.Assert(otx.FinaliseHeight, Equals, int64(200))
	c.Assert(otx.Status, Equals, Status_incomplete)
	c.Assert(otx.ObservedPubKey.Equals(pk), Equals, true)
}

func (s *CoverageSuite) TestObservedTxValid(c *C) {
	pk := getTestPubKey(c)
	tx := Tx{
		ID:          BlankTxID,
		Chain:       BTCChain,
		FromAddress: "from",
		ToAddress:   "to",
		Coins:       Coins{NewCoin(BTCAsset, cosmos.NewUint(100))},
		Gas:         Gas{NewCoin(BTCAsset, cosmos.NewUint(10))},
		Memo:        "test",
	}
	otx := NewObservedTx(tx, 100, pk, 200)
	c.Assert(otx.Valid(), IsNil)

	// invalid tx
	otx2 := NewObservedTx(Tx{}, 100, pk, 200)
	c.Assert(otx2.Valid(), NotNil)

	// zero block height
	otx3 := NewObservedTx(tx, 0, pk, 200)
	c.Assert(otx3.Valid(), NotNil)

	// empty pubkey
	otx4 := NewObservedTx(tx, 100, PubKey(""), 200)
	c.Assert(otx4.Valid(), NotNil)

	// zero finalise height
	otx5 := NewObservedTx(tx, 100, pk, 0)
	c.Assert(otx5.Valid(), NotNil)
}

func (s *CoverageSuite) TestObservedTxIsEmpty(c *C) {
	otx := &ObservedTx{}
	c.Assert(otx.IsEmpty(), Equals, true)

	otx2 := &ObservedTx{Tx: Tx{ID: BlankTxID}}
	c.Assert(otx2.IsEmpty(), Equals, false)
}

func (s *CoverageSuite) TestObservedTxEquals(c *C) {
	pk := getTestPubKey(c)
	tx := Tx{
		ID:          BlankTxID,
		Chain:       BTCChain,
		FromAddress: "from",
		ToAddress:   "to",
		Coins:       Coins{NewCoin(BTCAsset, cosmos.NewUint(100))},
		Gas:         Gas{NewCoin(BTCAsset, cosmos.NewUint(10))},
		Memo:        "test",
	}
	otx1 := NewObservedTx(tx, 100, pk, 200)
	otx2 := NewObservedTx(tx, 100, pk, 200)
	c.Assert(otx1.Equals(otx2), Equals, true)

	// different block height
	otx3 := NewObservedTx(tx, 101, pk, 200)
	c.Assert(otx1.Equals(otx3), Equals, false)

	// different finalise height
	otx4 := NewObservedTx(tx, 100, pk, 201)
	c.Assert(otx1.Equals(otx4), Equals, false)

	// different pubkey
	pk2 := getTestPubKey2(c)
	otx5 := NewObservedTx(tx, 100, pk2, 200)
	c.Assert(otx1.Equals(otx5), Equals, false)

	// different aggregator
	otx6 := NewObservedTx(tx, 100, pk, 200)
	otx6.Aggregator = "dex"
	c.Assert(otx1.Equals(otx6), Equals, false)

	// different aggregator target
	otx7 := NewObservedTx(tx, 100, pk, 200)
	otx7.AggregatorTarget = "target"
	c.Assert(otx1.Equals(otx7), Equals, false)

	// different aggregator target limit
	limit := cosmos.NewUint(500)
	otx8 := NewObservedTx(tx, 100, pk, 200)
	otx8.AggregatorTargetLimit = &limit
	c.Assert(otx1.Equals(otx8), Equals, false)

	// both have same aggregator target limit
	otx9 := NewObservedTx(tx, 100, pk, 200)
	limit2 := cosmos.NewUint(500)
	otx9.AggregatorTargetLimit = &limit2
	c.Assert(otx8.Equals(otx9), Equals, true)
}

func (s *CoverageSuite) TestObservedTxIsFinal(c *C) {
	otx := &ObservedTx{BlockHeight: 100, FinaliseHeight: 100}
	c.Assert(otx.IsFinal(), Equals, true)

	otx2 := &ObservedTx{BlockHeight: 100, FinaliseHeight: 200}
	c.Assert(otx2.IsFinal(), Equals, false)
}

func (s *CoverageSuite) TestObservedTxGetOutHashes(c *C) {
	otx := &ObservedTx{
		OutHashes: []string{
			"A7DA8FF1B7C290616D68A276F30AC618315E6CCE982EB8F7A79339E163798F49",
			"invalid",
		},
	}
	txIDs := otx.GetOutHashes()
	c.Assert(txIDs, HasLen, 1) // invalid one skipped
}

func (s *CoverageSuite) TestObservedTxSignersAndHasSigned(c *C) {
	acc := cosmos.AccAddress([]byte("test_address_12345678"))
	otx := &ObservedTx{}

	c.Assert(otx.HasSigned(acc), Equals, false)
	c.Assert(otx.Sign(acc), Equals, true)
	c.Assert(otx.HasSigned(acc), Equals, true)
	c.Assert(otx.Sign(acc), Equals, false) // already signed

	signers := otx.GetSigners()
	c.Assert(signers, HasLen, 1)
}

func (s *CoverageSuite) TestObservedTxSetDoneAndIsDone(c *C) {
	otx := &ObservedTx{}
	c.Assert(otx.IsDone(1), Equals, false)

	txID, _ := NewTxID("A7DA8FF1B7C290616D68A276F30AC618315E6CCE982EB8F7A79339E163798F49")
	otx.SetDone(txID, 1)
	c.Assert(otx.IsDone(1), Equals, true)
	c.Assert(otx.Status, Equals, Status_done)

	// duplicate hash should not be added
	otx.SetDone(txID, 1)
	c.Assert(len(otx.OutHashes), Equals, 1)

	// BlankTxID can be added multiple times
	otx2 := &ObservedTx{}
	otx2.SetDone(BlankTxID, 3)
	otx2.SetDone(BlankTxID, 3)
	otx2.SetDone(BlankTxID, 3)
	c.Assert(otx2.IsDone(3), Equals, true)
	c.Assert(otx2.Status, Equals, Status_done)
}

func (s *CoverageSuite) TestObservedTxString(c *C) {
	tx := Tx{
		ID:          BlankTxID,
		Chain:       BTCChain,
		FromAddress: "from",
		ToAddress:   "to",
		Coins:       Coins{NewCoin(BTCAsset, cosmos.NewUint(100))},
		Gas:         Gas{NewCoin(BTCAsset, cosmos.NewUint(10))},
		Memo:        "test",
	}
	otx := &ObservedTx{Tx: tx}
	c.Assert(len(otx.String()) > 0, Equals, true)
}

func (s *CoverageSuite) TestObservedTxsContains(c *C) {
	pk := getTestPubKey(c)
	tx := Tx{
		ID:          BlankTxID,
		Chain:       BTCChain,
		FromAddress: "from",
		ToAddress:   "to",
		Coins:       Coins{NewCoin(BTCAsset, cosmos.NewUint(100))},
		Gas:         Gas{NewCoin(BTCAsset, cosmos.NewUint(10))},
		Memo:        "test",
	}
	otx := NewObservedTx(tx, 100, pk, 200)
	txs := ObservedTxs{otx}
	c.Assert(txs.Contains(otx), Equals, true)

	otx2 := NewObservedTx(tx, 101, pk, 200)
	c.Assert(txs.Contains(otx2), Equals, false)
}

func (s *CoverageSuite) TestStatusJSON(c *C) {
	status := Status_done
	data, err := json.Marshal(status)
	c.Assert(err, IsNil)

	var status2 Status
	err = json.Unmarshal(data, &status2)
	c.Assert(err, IsNil)
	c.Assert(status2, Equals, Status_done)

	// invalid status
	var status3 Status
	err = json.Unmarshal([]byte(`"bogus"`), &status3)
	c.Assert(err, NotNil)

	// invalid json
	var status4 Status
	err = json.Unmarshal([]byte(`123`), &status4)
	c.Assert(err, NotNil)
}

// --- Attestation tests ---

func (s *CoverageSuite) TestAttestationEquals(c *C) {
	att1 := &Attestation{Signature: []byte("sig1"), PubKey: []byte("pk1")}
	att2 := &Attestation{Signature: []byte("sig1"), PubKey: []byte("pk1")}
	c.Assert(att1.Equals(att2), Equals, true)

	att3 := &Attestation{Signature: []byte("sig2"), PubKey: []byte("pk1")}
	c.Assert(att1.Equals(att3), Equals, false)

	att4 := &Attestation{Signature: []byte("sig1"), PubKey: []byte("pk2")}
	c.Assert(att1.Equals(att4), Equals, false)
}

// --- NetworkFee tests ---

func (s *CoverageSuite) TestNetworkFeeValid(c *C) {
	nf := &NetworkFee{Chain: "BTC", Height: 100, TransactionSize: 250, TransactionRate: 10}
	c.Assert(nf.Valid(), IsNil)

	nf2 := &NetworkFee{Chain: "", Height: 100, TransactionSize: 250, TransactionRate: 10}
	c.Assert(nf2.Valid(), NotNil)

	nf3 := &NetworkFee{Chain: "BTC", Height: 0, TransactionSize: 250, TransactionRate: 10}
	c.Assert(nf3.Valid(), NotNil)

	nf4 := &NetworkFee{Chain: "BTC", Height: 100, TransactionSize: 0, TransactionRate: 10}
	c.Assert(nf4.Valid(), NotNil)

	nf5 := &NetworkFee{Chain: "BTC", Height: 100, TransactionSize: 250, TransactionRate: 0}
	c.Assert(nf5.Valid(), NotNil)
}

func (s *CoverageSuite) TestNetworkFeeIsValid(c *C) {
	nf := &NetworkFee{}
	c.Assert(nf.IsValid(), Equals, true)

	var nilNF *NetworkFee
	c.Assert(nilNF.IsValid(), Equals, false)
}

func (s *CoverageSuite) TestNetworkFeeGetSignablePayload(c *C) {
	nf := &NetworkFee{Chain: "BTC", Height: 100, TransactionSize: 250, TransactionRate: 10}
	payload, err := nf.GetSignablePayload()
	c.Assert(err, IsNil)
	c.Assert(len(payload) > 0, Equals, true)
}

func (s *CoverageSuite) TestNetworkFeeEquals(c *C) {
	nf1 := &NetworkFee{Chain: "BTC", Height: 100, TransactionSize: 250, TransactionRate: 10}
	nf2 := &NetworkFee{Chain: "BTC", Height: 100, TransactionSize: 250, TransactionRate: 10}
	c.Assert(nf1.Equals(nf2), Equals, true)

	nf3 := &NetworkFee{Chain: "ETH", Height: 100, TransactionSize: 250, TransactionRate: 10}
	c.Assert(nf1.Equals(nf3), Equals, false)

	nf4 := &NetworkFee{Chain: "BTC", Height: 101, TransactionSize: 250, TransactionRate: 10}
	c.Assert(nf1.Equals(nf4), Equals, false)

	nf5 := &NetworkFee{Chain: "BTC", Height: 100, TransactionSize: 251, TransactionRate: 10}
	c.Assert(nf1.Equals(nf5), Equals, false)

	nf6 := &NetworkFee{Chain: "BTC", Height: 100, TransactionSize: 250, TransactionRate: 11}
	c.Assert(nf1.Equals(nf6), Equals, false)
}

// --- Solvency tests ---

func (s *CoverageSuite) TestSolvencyHash(c *C) {
	sol := &Solvency{
		Chain:  "BTC",
		PubKey: "pk1",
		Coins:  Coins{NewCoin(BTCAsset, cosmos.NewUint(100))},
		Height: 100,
	}
	txID, err := sol.Hash()
	c.Assert(err, IsNil)
	c.Assert(txID.IsEmpty(), Equals, false)
}

func (s *CoverageSuite) TestSolvencyIsValid(c *C) {
	sol := &Solvency{}
	c.Assert(sol.IsValid(), Equals, true)

	var nilSol *Solvency
	c.Assert(nilSol.IsValid(), Equals, false)
}

func (s *CoverageSuite) TestSolvencyGetSignablePayload(c *C) {
	sol := &Solvency{
		Chain:  "BTC",
		PubKey: "pk1",
		Coins:  Coins{NewCoin(BTCAsset, cosmos.NewUint(100))},
		Height: 100,
	}
	payload, err := sol.GetSignablePayload()
	c.Assert(err, IsNil)
	c.Assert(len(payload) > 0, Equals, true)

	// test with empty Id (should compute hash)
	sol2 := &Solvency{
		Chain:  "BTC",
		PubKey: "pk1",
		Coins:  Coins{NewCoin(BTCAsset, cosmos.NewUint(100))},
		Height: 100,
	}
	sol2.Id = ""
	payload2, err := sol2.GetSignablePayload()
	c.Assert(err, IsNil)
	c.Assert(len(payload2) > 0, Equals, true)
	c.Assert(sol2.Id.IsEmpty(), Equals, false) // Id should now be set
}

func (s *CoverageSuite) TestSolvencyEquals(c *C) {
	sol1 := &Solvency{
		Chain:  "BTC",
		PubKey: "pk1",
		Coins:  Coins{NewCoin(BTCAsset, cosmos.NewUint(100))},
		Height: 100,
	}
	sol2 := &Solvency{
		Chain:  "BTC",
		PubKey: "pk1",
		Coins:  Coins{NewCoin(BTCAsset, cosmos.NewUint(100))},
		Height: 100,
	}
	c.Assert(sol1.Equals(sol2), Equals, true)

	sol3 := &Solvency{Chain: "ETH", PubKey: "pk1", Coins: Coins{NewCoin(BTCAsset, cosmos.NewUint(100))}, Height: 100}
	c.Assert(sol1.Equals(sol3), Equals, false)

	sol4 := &Solvency{Chain: "BTC", PubKey: "pk2", Coins: Coins{NewCoin(BTCAsset, cosmos.NewUint(100))}, Height: 100}
	c.Assert(sol1.Equals(sol4), Equals, false)

	sol5 := &Solvency{Chain: "BTC", PubKey: "pk1", Coins: Coins{NewCoin(BTCAsset, cosmos.NewUint(100))}, Height: 101}
	c.Assert(sol1.Equals(sol5), Equals, false)

	sol6 := &Solvency{Chain: "BTC", PubKey: "pk1", Coins: Coins{NewCoin(BTCAsset, cosmos.NewUint(200))}, Height: 100}
	c.Assert(sol1.Equals(sol6), Equals, false)

	sol7 := &Solvency{Chain: "BTC", PubKey: "pk1", Coins: Coins{}, Height: 100}
	c.Assert(sol1.Equals(sol7), Equals, false)
}

// --- ErrataTx tests ---

func (s *CoverageSuite) TestErrataTxIsValid(c *C) {
	e := &ErrataTx{}
	c.Assert(e.IsValid(), Equals, true)

	var nilE *ErrataTx
	c.Assert(nilE.IsValid(), Equals, false)
}

func (s *CoverageSuite) TestErrataTxGetSignablePayload(c *C) {
	e := &ErrataTx{Chain: "BTC", Id: "A7DA8FF1B7C290616D68A276F30AC618315E6CCE982EB8F7A79339E163798F49"}
	payload, err := e.GetSignablePayload()
	c.Assert(err, IsNil)
	c.Assert(len(payload) > 0, Equals, true)
}

func (s *CoverageSuite) TestErrataTxEquals(c *C) {
	e1 := &ErrataTx{Chain: "BTC", Id: "txid1"}
	e2 := &ErrataTx{Chain: "BTC", Id: "txid1"}
	c.Assert(e1.Equals(e2), Equals, true)

	e3 := &ErrataTx{Chain: "ETH", Id: "txid1"}
	c.Assert(e1.Equals(e3), Equals, false)

	e4 := &ErrataTx{Chain: "BTC", Id: "txid2"}
	c.Assert(e1.Equals(e4), Equals, false)
}

// --- PriceFeed tests ---

func (s *CoverageSuite) TestPriceFeedValid(c *C) {
	pf := &PriceFeed{
		Rates:   []*OraclePrice{{Amount: 100, Decimals: 8}},
		Time:    1000,
		Version: []byte{1, 2, 3, 4, 5, 6, 7, 8},
	}
	c.Assert(pf.Valid(), IsNil)

	pf2 := &PriceFeed{Rates: []*OraclePrice{}, Time: 1000, Version: []byte{1, 2, 3, 4, 5, 6, 7, 8}}
	c.Assert(pf2.Valid(), NotNil)

	pf3 := &PriceFeed{Rates: []*OraclePrice{{Amount: 100}}, Time: 0, Version: []byte{1, 2, 3, 4, 5, 6, 7, 8}}
	c.Assert(pf3.Valid(), NotNil)

	pf4 := &PriceFeed{Rates: []*OraclePrice{{Amount: 100}}, Time: 1000, Version: []byte{1, 2, 3}}
	c.Assert(pf4.Valid(), NotNil)
}

func (s *CoverageSuite) TestPriceFeedIsValid(c *C) {
	pf := &PriceFeed{}
	c.Assert(pf.IsValid(), Equals, true)

	var nilPF *PriceFeed
	c.Assert(nilPF.IsValid(), Equals, false)
}

func (s *CoverageSuite) TestPriceFeedGetSignablePayload(c *C) {
	pf := &PriceFeed{
		Rates:   []*OraclePrice{{Amount: 100, Decimals: 8}},
		Time:    1000,
		Version: []byte{1, 2, 3, 4, 5, 6, 7, 8},
	}
	payload, err := pf.GetSignablePayload()
	c.Assert(err, IsNil)
	c.Assert(len(payload) > 0, Equals, true)
}

func (s *CoverageSuite) TestPriceFeedEquals(c *C) {
	pf1 := &PriceFeed{
		Rates:   []*OraclePrice{{Amount: 100, Decimals: 8}},
		Time:    1000,
		Version: []byte{1, 2, 3, 4, 5, 6, 7, 8},
	}
	pf2 := &PriceFeed{
		Rates:   []*OraclePrice{{Amount: 100, Decimals: 8}},
		Time:    1000,
		Version: []byte{1, 2, 3, 4, 5, 6, 7, 8},
	}
	c.Assert(pf1.Equals(pf2), Equals, true)

	// nil comparison
	c.Assert(pf1.Equals(nil), Equals, false)
	var nilPF *PriceFeed
	c.Assert(nilPF.Equals(pf1), Equals, false)

	// different time
	pf3 := &PriceFeed{Rates: []*OraclePrice{{Amount: 100, Decimals: 8}}, Time: 2000, Version: []byte{1, 2, 3, 4, 5, 6, 7, 8}}
	c.Assert(pf1.Equals(pf3), Equals, false)

	// different version
	pf4 := &PriceFeed{Rates: []*OraclePrice{{Amount: 100, Decimals: 8}}, Time: 1000, Version: []byte{8, 7, 6, 5, 4, 3, 2, 1}}
	c.Assert(pf1.Equals(pf4), Equals, false)

	// different rates count
	pf5 := &PriceFeed{Rates: []*OraclePrice{{Amount: 100, Decimals: 8}, {Amount: 200, Decimals: 6}}, Time: 1000, Version: []byte{1, 2, 3, 4, 5, 6, 7, 8}}
	c.Assert(pf1.Equals(pf5), Equals, false)

	// different rate amount
	pf6 := &PriceFeed{Rates: []*OraclePrice{{Amount: 200, Decimals: 8}}, Time: 1000, Version: []byte{1, 2, 3, 4, 5, 6, 7, 8}}
	c.Assert(pf1.Equals(pf6), Equals, false)

	// different rate decimals
	pf7 := &PriceFeed{Rates: []*OraclePrice{{Amount: 100, Decimals: 6}}, Time: 1000, Version: []byte{1, 2, 3, 4, 5, 6, 7, 8}}
	c.Assert(pf1.Equals(pf7), Equals, false)
}

// --- Quorum type tests ---

func (s *CoverageSuite) TestQuorumTx(c *C) {
	atts := []*Attestation{{Signature: []byte("sig1"), PubKey: []byte("pk1")}}
	tx := Tx{
		ID:          BlankTxID,
		Chain:       BTCChain,
		FromAddress: "from",
		ToAddress:   "to",
		Coins:       Coins{NewCoin(BTCAsset, cosmos.NewUint(100))},
		Gas:         Gas{NewCoin(BTCAsset, cosmos.NewUint(10))},
		Memo:        "test",
	}
	otx := ObservedTx{Tx: tx}
	qtx := &QuorumTx{ObsTx: otx, Inbound: true, Attestations: atts}

	c.Assert(qtx.GetAttestations(), HasLen, 1)

	newAtts := []*Attestation{{Signature: []byte("sig2"), PubKey: []byte("pk2")}}
	qtx.SetAttestations(newAtts)
	c.Assert(qtx.GetAttestations(), HasLen, 1)

	// Equals
	qtx2 := &QuorumTx{ObsTx: otx, Inbound: true}
	c.Assert(qtx.Equals(qtx2), Equals, true)

	qtx3 := &QuorumTx{ObsTx: otx, Inbound: false}
	c.Assert(qtx.Equals(qtx3), Equals, false)

	// RemoveAttestations
	removed := qtx.RemoveAttestations(newAtts)
	c.Assert(removed, Equals, true)
	c.Assert(qtx.GetAttestations(), HasLen, 0)
}

func (s *CoverageSuite) TestQuorumNetworkFee(c *C) {
	atts := []*Attestation{{Signature: []byte("sig1"), PubKey: []byte("pk1")}}
	nf := &NetworkFee{Chain: "BTC", Height: 100, TransactionSize: 250, TransactionRate: 10}
	qnf := &QuorumNetworkFee{NetworkFee: nf, Attestations: atts}

	c.Assert(qnf.GetAttestations(), HasLen, 1)

	newAtts := []*Attestation{{Signature: []byte("sig2"), PubKey: []byte("pk2")}}
	qnf.SetAttestations(newAtts)
	c.Assert(qnf.GetAttestations(), HasLen, 1)

	// Equals
	qnf2 := &QuorumNetworkFee{NetworkFee: &NetworkFee{Chain: "BTC", Height: 100, TransactionSize: 250, TransactionRate: 10}}
	c.Assert(qnf.Equals(qnf2), Equals, true)

	// RemoveAttestations
	removed := qnf.RemoveAttestations(newAtts)
	c.Assert(removed, Equals, true)
}

func (s *CoverageSuite) TestQuorumSolvency(c *C) {
	atts := []*Attestation{{Signature: []byte("sig1"), PubKey: []byte("pk1")}}
	sol := &Solvency{Chain: "BTC", PubKey: "pk1", Height: 100}
	qs := &QuorumSolvency{Solvency: sol, Attestations: atts}

	c.Assert(qs.GetAttestations(), HasLen, 1)

	newAtts := []*Attestation{{Signature: []byte("sig2"), PubKey: []byte("pk2")}}
	qs.SetAttestations(newAtts)
	c.Assert(qs.GetAttestations(), HasLen, 1)

	// Equals
	qs2 := &QuorumSolvency{Solvency: &Solvency{Chain: "BTC", PubKey: "pk1", Height: 100}}
	c.Assert(qs.Equals(qs2), Equals, true)

	// RemoveAttestations
	removed := qs.RemoveAttestations(newAtts)
	c.Assert(removed, Equals, true)
}

func (s *CoverageSuite) TestQuorumErrataTx(c *C) {
	atts := []*Attestation{{Signature: []byte("sig1"), PubKey: []byte("pk1")}}
	e := &ErrataTx{Chain: "BTC", Id: "txid1"}
	qe := &QuorumErrataTx{ErrataTx: e, Attestations: atts}

	c.Assert(qe.GetAttestations(), HasLen, 1)

	newAtts := []*Attestation{{Signature: []byte("sig2"), PubKey: []byte("pk2")}}
	qe.SetAttestations(newAtts)
	c.Assert(qe.GetAttestations(), HasLen, 1)

	// Equals
	qe2 := &QuorumErrataTx{ErrataTx: &ErrataTx{Chain: "BTC", Id: "txid1"}}
	c.Assert(qe.Equals(qe2), Equals, true)

	// RemoveAttestations
	removed := qe.RemoveAttestations(newAtts)
	c.Assert(removed, Equals, true)
}

func (s *CoverageSuite) TestQuorumPriceFeedBatch(c *C) {
	atts := []*Attestation{{Signature: []byte("sig1"), PubKey: []byte("pk1")}}
	qpfb := &QuorumPriceFeedBatch{
		QuorumPriceFeeds: []*QuorumPriceFeed{
			{Attestations: atts},
		},
	}

	c.Assert(qpfb.GetAttestations(), HasLen, 1)

	// SetAttestations is a no-op
	qpfb.SetAttestations([]*Attestation{{Signature: []byte("sig2")}})
	c.Assert(qpfb.GetAttestations(), HasLen, 1) // unchanged

	// RemoveAttestations always returns true
	c.Assert(qpfb.RemoveAttestations(atts), Equals, true)

	// Equals always returns false
	c.Assert(qpfb.Equals(&QuorumPriceFeedBatch{}), Equals, false)

	// empty QuorumPriceFeedBatch
	emptyBatch := &QuorumPriceFeedBatch{}
	c.Assert(emptyBatch.GetAttestations(), IsNil)
}

func (s *CoverageSuite) TestRemoveAttestations(c *C) {
	att1 := &Attestation{Signature: []byte("sig1"), PubKey: []byte("pk1")}
	att2 := &Attestation{Signature: []byte("sig2"), PubKey: []byte("pk2")}
	att3 := &Attestation{Signature: []byte("sig3"), PubKey: []byte("pk3")}

	existing := []*Attestation{att1, att2, att3}
	toRemove := []*Attestation{att2}
	result := removeAttestations(existing, toRemove)
	c.Assert(result, HasLen, 2)
}

// --- Attest* type tests ---

func (s *CoverageSuite) TestAttestTx(c *C) {
	att := &Attestation{Signature: []byte("sig"), PubKey: []byte("pk")}
	tx := Tx{
		ID:          BlankTxID,
		Chain:       BTCChain,
		FromAddress: "from",
		ToAddress:   "to",
		Coins:       Coins{NewCoin(BTCAsset, cosmos.NewUint(100))},
		Gas:         Gas{NewCoin(BTCAsset, cosmos.NewUint(10))},
		Memo:        "test",
	}
	atx := &AttestTx{
		Attestation: att,
		ObsTx:       ObservedTx{Tx: tx},
	}
	c.Assert(atx.GetAttestation(), Equals, att)
	payload, err := atx.GetSignablePayload()
	c.Assert(err, IsNil)
	c.Assert(len(payload) > 0, Equals, true)
}

func (s *CoverageSuite) TestAttestNetworkFee(c *C) {
	att := &Attestation{Signature: []byte("sig"), PubKey: []byte("pk")}
	anf := &AttestNetworkFee{
		Attestation: att,
		NetworkFee:  &NetworkFee{Chain: "BTC", Height: 100, TransactionSize: 250, TransactionRate: 10},
	}
	c.Assert(anf.GetAttestation(), Equals, att)
	payload, err := anf.GetSignablePayload()
	c.Assert(err, IsNil)
	c.Assert(len(payload) > 0, Equals, true)
}

func (s *CoverageSuite) TestAttestSolvency(c *C) {
	att := &Attestation{Signature: []byte("sig"), PubKey: []byte("pk")}
	as := &AttestSolvency{
		Attestation: att,
		Solvency:    &Solvency{Chain: "BTC", PubKey: "pk1", Height: 100},
	}
	c.Assert(as.GetAttestation(), Equals, att)
	payload, err := as.GetSignablePayload()
	c.Assert(err, IsNil)
	c.Assert(len(payload) > 0, Equals, true)
}

func (s *CoverageSuite) TestAttestErrataTx(c *C) {
	att := &Attestation{Signature: []byte("sig"), PubKey: []byte("pk")}
	ae := &AttestErrataTx{
		Attestation: att,
		ErrataTx:    &ErrataTx{Chain: "BTC", Id: "txid"},
	}
	c.Assert(ae.GetAttestation(), Equals, att)
	payload, err := ae.GetSignablePayload()
	c.Assert(err, IsNil)
	c.Assert(len(payload) > 0, Equals, true)
}

// --- Account tests ---

func (s *CoverageSuite) TestNewAccount(c *C) {
	coins := Coins{NewCoin(BTCAsset, cosmos.NewUint(100))}
	acc := NewAccount(1, 2, coins, true)
	c.Assert(acc.Sequence, Equals, int64(1))
	c.Assert(acc.AccountNumber, Equals, int64(2))
	c.Assert(acc.HasMemoFlag, Equals, true)
	c.Assert(acc.Coins, HasLen, 1)
}

// --- Fee tests ---

func (s *CoverageSuite) TestNewFee(c *C) {
	coins := Coins{NewCoin(BTCAsset, cosmos.NewUint(100))}
	fee := NewFee(coins, cosmos.NewUint(50))
	c.Assert(fee.Coins, HasLen, 1)
	c.Assert(fee.PoolDeduct.Equal(cosmos.NewUint(50)), Equals, true)
}

func (s *CoverageSuite) TestFeeString(c *C) {
	coins := Coins{NewCoin(BTCAsset, cosmos.NewUint(100))}
	fee := NewFee(coins, cosmos.NewUint(50))
	str := fee.String()
	c.Assert(len(str) > 0, Equals, true)
	c.Check(str, Equals, "50: 100 BTC.BTC")
}

// --- InvariantRoute tests ---

func (s *CoverageSuite) TestNewInvariantRoute(c *C) {
	inv := func(_ cosmos.Context) ([]string, bool) {
		return nil, false
	}
	route := NewInvariantRoute("test", inv)
	c.Assert(route.Route, Equals, "test")
	c.Assert(route.Invariant, NotNil)
}

// --- RandHexString tests ---

func (s *CoverageSuite) TestRandHexString(c *C) {
	s1 := RandHexString(10)
	c.Assert(len(s1), Equals, 10)

	s2 := RandHexString(0)
	c.Assert(len(s2), Equals, 0)

	// two random strings should be different (with very high probability)
	s3 := RandHexString(32)
	s4 := RandHexString(32)
	c.Assert(s3 != s4, Equals, true)
}

// Every character is uniform over hexBytes. Taking b%len(hexBytes) of every
// byte, without redrawing the top 256%22 values, makes the last 8 symbols ~5.5%
// rare: ~2700 short here, against a tolerance of 1500 (~7 sigma).
func (s *CoverageSuite) TestRandHexStringUniform(c *C) {
	const perSymbol = 50_000
	var counts [256]int
	for _, ch := range []byte(RandHexString(perSymbol * len(hexBytes))) {
		counts[ch]++
	}
	total := 0
	for i := 0; i < len(hexBytes); i++ {
		got := counts[hexBytes[i]]
		total += got
		c.Check(got > perSymbol-1500 && got < perSymbol+1500, Equals, true,
			Commentf("%q drawn %d times, want ~%d", hexBytes[i], got, perSymbol))
	}
	c.Assert(total, Equals, perSymbol*len(hexBytes), Commentf("characters outside hexBytes"))
}

// --- ChainPoolInfo tests ---

func (s *CoverageSuite) TestNewChainPoolInfo(c *C) {
	// empty chain
	_, err := NewChainPoolInfo(EmptyChain, getTestPubKey(c))
	c.Assert(err, NotNil)

	// empty pubkey
	_, err = NewChainPoolInfo(BTCChain, PubKey(""))
	c.Assert(err, NotNil)

	// valid
	pk := getTestPubKey(c)
	cpi, err := NewChainPoolInfo(THORChain, pk)
	c.Assert(err, IsNil)
	c.Assert(cpi.Chain.Equals(THORChain), Equals, true)
	c.Assert(cpi.IsEmpty(), Equals, false)

	// empty cpi
	c.Assert(EmptyChainPoolInfo.IsEmpty(), Equals, true)
}

// --- Entropy tests ---

func (s *CoverageSuite) TestEntropy(c *C) {
	// zero entropy (all same bytes)
	e := Entropy([]byte{0, 0, 0, 0, 0, 0, 0, 0})
	c.Assert(e >= 0, Equals, true)

	// high entropy
	data := make([]byte, 256)
	for i := range data {
		data[i] = byte(i)
	}
	e2 := Entropy(data)
	c.Assert(e2 > e, Equals, true)
}

// --- Additional Coin tests ---

func (s *CoverageSuite) TestCoinString(c *C) {
	coin := NewCoin(BTCAsset, cosmos.NewUint(12345))
	c.Assert(coin.String(), Equals, "12345 BTC.BTC")
}

func (s *CoverageSuite) TestCoinWithDecimals(c *C) {
	coin := NewCoin(BTCAsset, cosmos.NewUint(100))
	coin2 := coin.WithDecimals(18)
	c.Assert(coin2.Decimals, Equals, int64(18))
}

func (s *CoverageSuite) TestCoinIsRune(c *C) {
	coin := NewCoin(RuneNative, cosmos.NewUint(100))
	c.Assert(coin.IsRune(), Equals, true)

	coin2 := NewCoin(BTCAsset, cosmos.NewUint(100))
	c.Assert(coin2.IsRune(), Equals, false)
}

func (s *CoverageSuite) TestCoinIsTCY(c *C) {
	coin := NewCoin(TCY, cosmos.NewUint(100))
	c.Assert(coin.IsTCY(), Equals, true)

	coin2 := NewCoin(BTCAsset, cosmos.NewUint(100))
	c.Assert(coin2.IsTCY(), Equals, false)
}

func (s *CoverageSuite) TestParseCoin(c *C) {
	coin, err := ParseCoin("100 BTC.BTC")
	c.Assert(err, IsNil)
	c.Assert(coin.Asset.Equals(BTCAsset), Equals, true)
	c.Assert(coin.Amount.Equal(cosmos.NewUint(100)), Equals, true)

	// invalid format
	_, err = ParseCoin("invalid")
	c.Assert(err, NotNil)

	// invalid amount
	_, err = ParseCoin("abc BTC.BTC")
	c.Assert(err, NotNil)

	// invalid asset (empty string)
	_, err = ParseCoin("100 ")
	c.Assert(err, NotNil)
}

func (s *CoverageSuite) TestCoinsEqualsEx(c *C) {
	c1 := Coins{NewCoin(BTCAsset, cosmos.NewUint(100)), NewCoin(ETHAsset, cosmos.NewUint(200))}
	c2 := Coins{NewCoin(ETHAsset, cosmos.NewUint(200)), NewCoin(BTCAsset, cosmos.NewUint(100))}
	c.Assert(c1.EqualsEx(c2), Equals, true)

	c3 := Coins{NewCoin(BTCAsset, cosmos.NewUint(100))}
	c.Assert(c1.EqualsEx(c3), Equals, false)

	c4 := Coins{NewCoin(BTCAsset, cosmos.NewUint(100)), NewCoin(ETHAsset, cosmos.NewUint(300))}
	c.Assert(c1.EqualsEx(c4), Equals, false)
}

func (s *CoverageSuite) TestCoinsIsEmpty(c *C) {
	c.Assert(Coins{}.IsEmpty(), Equals, true)
	c.Assert(Coins{NoCoin}.IsEmpty(), Equals, true)
	c.Assert(Coins{NewCoin(BTCAsset, cosmos.NewUint(100))}.IsEmpty(), Equals, false)
}

func (s *CoverageSuite) TestCoinsNative(c *C) {
	coins := Coins{NewCoin(RuneNative, cosmos.NewUint(100))}
	native, err := coins.Native()
	c.Assert(err, IsNil)
	c.Assert(native, HasLen, 1)

	// non-native coins should error
	coins2 := Coins{NewCoin(BTCAsset, cosmos.NewUint(100))}
	_, err = coins2.Native()
	c.Assert(err, NotNil)
}

func (s *CoverageSuite) TestCoinsString(c *C) {
	coins := Coins{NewCoin(BTCAsset, cosmos.NewUint(100)), NewCoin(ETHAsset, cosmos.NewUint(200))}
	str := coins.String()
	c.Assert(len(str) > 0, Equals, true)
}

func (s *CoverageSuite) TestCoinsContains(c *C) {
	coins := Coins{NewCoin(BTCAsset, cosmos.NewUint(100))}
	c.Assert(coins.Contains(NewCoin(BTCAsset, cosmos.NewUint(100))), Equals, true)
	c.Assert(coins.Contains(NewCoin(ETHAsset, cosmos.NewUint(100))), Equals, false)
}

func (s *CoverageSuite) TestCoinsSafeSub(c *C) {
	coins := Coins{NewCoin(BTCAsset, cosmos.NewUint(100)), NewCoin(ETHAsset, cosmos.NewUint(200))}
	result := coins.SafeSub(NewCoin(BTCAsset, cosmos.NewUint(30)))
	c.Assert(result.GetCoin(BTCAsset).Amount.Equal(cosmos.NewUint(70)), Equals, true)
	c.Assert(result.GetCoin(ETHAsset).Amount.Equal(cosmos.NewUint(200)), Equals, true)

	// original should be unchanged
	c.Assert(coins.GetCoin(BTCAsset).Amount.Equal(cosmos.NewUint(100)), Equals, true)

	// subtract more than available
	result2 := coins.SafeSub(NewCoin(BTCAsset, cosmos.NewUint(200)))
	c.Assert(result2.GetCoin(BTCAsset).Amount.Equal(cosmos.ZeroUint()), Equals, true)
}

func (s *CoverageSuite) TestCoinsCopy(c *C) {
	coins := Coins{NewCoin(BTCAsset, cosmos.NewUint(100))}
	copied := coins.Copy()
	c.Assert(copied, HasLen, 1)
	c.Assert(copied[0].Amount.Equal(cosmos.NewUint(100)), Equals, true)
}

func (s *CoverageSuite) TestCoinsValid(c *C) {
	coins := Coins{NewCoin(BTCAsset, cosmos.NewUint(100))}
	c.Assert(coins.Valid(), IsNil)

	coins2 := Coins{NewCoin(EmptyAsset, cosmos.NewUint(100))}
	c.Assert(coins2.Valid(), NotNil)
}

// --- Additional Gas tests ---

func (s *CoverageSuite) TestGasValid(c *C) {
	gas := Gas{NewCoin(BTCAsset, cosmos.NewUint(100))}
	c.Assert(gas.Valid(), IsNil)

	gas2 := Gas{NewCoin(EmptyAsset, cosmos.NewUint(100))}
	c.Assert(gas2.Valid(), NotNil)
}

func (s *CoverageSuite) TestGasEquals(c *C) {
	gas1 := Gas{NewCoin(BTCAsset, cosmos.NewUint(100))}
	gas2 := Gas{NewCoin(BTCAsset, cosmos.NewUint(100))}
	c.Assert(gas1.Equals(gas2), Equals, true)

	gas3 := Gas{NewCoin(BTCAsset, cosmos.NewUint(200))}
	c.Assert(gas1.Equals(gas3), Equals, false)

	gas4 := Gas{NewCoin(BTCAsset, cosmos.NewUint(100)), NewCoin(ETHAsset, cosmos.NewUint(50))}
	c.Assert(gas1.Equals(gas4), Equals, false)
}

func (s *CoverageSuite) TestGasToCoins(c *C) {
	gas := Gas{NewCoin(BTCAsset, cosmos.NewUint(100)), NewCoin(ETHAsset, cosmos.NewUint(200))}
	coins := gas.ToCoins()
	c.Assert(coins, HasLen, 2)
	c.Assert(coins[0].Asset.Equals(BTCAsset), Equals, true)
	c.Assert(coins[1].Asset.Equals(ETHAsset), Equals, true)
}

func (s *CoverageSuite) TestGasNoneEmpty(c *C) {
	gas := Gas{
		NewCoin(BTCAsset, cosmos.NewUint(100)),
		NewCoin(ETHAsset, cosmos.ZeroUint()),
	}
	result := gas.NoneEmpty()
	c.Assert(result, HasLen, 1)
	c.Assert(result[0].Asset.Equals(BTCAsset), Equals, true)
}

func (s *CoverageSuite) TestGasAddsDeprecated(c *C) {
	gas1 := Gas{NewCoin(BTCAsset, cosmos.NewUint(100))}
	gas2 := Gas{NewCoin(BTCAsset, cosmos.NewUint(50)), NewCoin(ETHAsset, cosmos.NewUint(200))}
	result := gas1.Adds_deprecated(gas2)
	c.Assert(result, HasLen, 2)
}

func (s *CoverageSuite) TestGasSafeSub(c *C) {
	gas := Gas{NewCoin(BTCAsset, cosmos.NewUint(100))}
	result := gas.SafeSub(NewCoin(BTCAsset, cosmos.NewUint(30)))
	c.Assert(result[0].Amount.Equal(cosmos.NewUint(70)), Equals, true)
}

// --- Additional Tx tests ---

func (s *CoverageSuite) TestTxHash(c *C) {
	id, _ := NewTxID("A7DA8FF1B7C290616D68A276F30AC618315E6CCE982EB8F7A79339E163798F49")
	tx := NewTx(
		id,
		Address("from"),
		Address("to"),
		Coins{NewCoin(BTCAsset, cosmos.NewUint(100))},
		Gas{NewCoin(BTCAsset, cosmos.NewUint(10))},
		"test",
	)
	hash := tx.Hash(100)
	c.Assert(len(hash), Equals, 64)
}

func (s *CoverageSuite) TestTxValid(c *C) {
	id, _ := NewTxID("A7DA8FF1B7C290616D68A276F30AC618315E6CCE982EB8F7A79339E163798F49")
	tx := NewTx(
		id,
		Address("from"),
		Address("to"),
		Coins{NewCoin(BTCAsset, cosmos.NewUint(100))},
		Gas{NewCoin(BTCAsset, cosmos.NewUint(10))},
		"test",
	)
	c.Assert(tx.Valid(), IsNil)

	// empty ID
	tx2 := tx
	tx2.ID = ""
	c.Assert(tx2.Valid(), NotNil)

	// empty from address
	tx3 := NewTx(id, Address(""), Address("to"), Coins{NewCoin(BTCAsset, cosmos.NewUint(100))}, Gas{NewCoin(BTCAsset, cosmos.NewUint(10))}, "test")
	c.Assert(tx3.Valid(), NotNil)

	// empty to address
	tx4 := NewTx(id, Address("from"), Address(""), Coins{NewCoin(BTCAsset, cosmos.NewUint(100))}, Gas{NewCoin(BTCAsset, cosmos.NewUint(10))}, "test")
	c.Assert(tx4.Valid(), NotNil)

	// empty chain
	tx5 := Tx{ID: id, FromAddress: "from", ToAddress: "to", Coins: Coins{NewCoin(BTCAsset, cosmos.NewUint(100))}, Gas: Gas{NewCoin(BTCAsset, cosmos.NewUint(10))}}
	c.Assert(tx5.Valid(), NotNil)

	// no coins
	tx6 := Tx{ID: id, Chain: BTCChain, FromAddress: "from", ToAddress: "to", Coins: Coins{}, Gas: Gas{NewCoin(BTCAsset, cosmos.NewUint(10))}}
	c.Assert(tx6.Valid(), NotNil)

	// no gas for non-thorchain tx
	tx7 := Tx{ID: id, Chain: BTCChain, FromAddress: "from", ToAddress: "to", Coins: Coins{NewCoin(BTCAsset, cosmos.NewUint(100))}, Gas: Gas{}}
	c.Assert(tx7.Valid(), NotNil)

	// THORChain tx without gas is ok
	tx8 := Tx{ID: id, Chain: THORChain, FromAddress: "from", ToAddress: "to", Coins: Coins{NewCoin(RuneNative, cosmos.NewUint(100))}, Gas: Gas{}}
	c.Assert(tx8.Valid(), IsNil)
}

func (s *CoverageSuite) TestTxToAttributes(c *C) {
	id, _ := NewTxID("A7DA8FF1B7C290616D68A276F30AC618315E6CCE982EB8F7A79339E163798F49")
	tx := NewTx(
		id,
		Address("from"),
		Address("to"),
		Coins{NewCoin(BTCAsset, cosmos.NewUint(100))},
		Gas{NewCoin(BTCAsset, cosmos.NewUint(10))},
		"test",
	)
	attrs := tx.ToAttributes()
	c.Assert(attrs, HasLen, 6)
}

func (s *CoverageSuite) TestGetRagnarokTx(c *C) {
	tx := GetRagnarokTx(BTCChain, Address("from"), Address("to"))
	c.Assert(tx.Chain.Equals(BTCChain), Equals, true)
	c.Assert(tx.ID.Equals(BlankTxID), Equals, true)
	c.Assert(tx.Memo, Equals, "Ragnarok")
}

func (s *CoverageSuite) TestTxIDIsBlank(c *C) {
	c.Assert(BlankTxID.IsBlank(), Equals, true)

	id, _ := NewTxID("A7DA8FF1B7C290616D68A276F30AC618315E6CCE982EB8F7A79339E163798F49")
	c.Assert(id.IsBlank(), Equals, false)
}

// --- Additional Chain tests ---

func (s *CoverageSuite) TestChainValid(c *C) {
	c.Assert(Chain("BTC").Valid(), IsNil)
	c.Assert(Chain("AB").Valid(), NotNil)          // too short
	c.Assert(Chain("ABCDEFGHIJK").Valid(), NotNil) // too long
	c.Assert(Chain("btc").Valid(), NotNil)         // lowercase
	c.Assert(Chain("BT1").Valid(), NotNil)         // contains digit
}

func (s *CoverageSuite) TestChainIsTHORChain(c *C) {
	c.Assert(THORChain.IsTHORChain(), Equals, true)
	c.Assert(BTCChain.IsTHORChain(), Equals, false)
}

func (s *CoverageSuite) TestChainIsBSCChain(c *C) {
	c.Assert(BSCChain.IsBSCChain(), Equals, true)
	c.Assert(BTCChain.IsBSCChain(), Equals, false)
}

func (s *CoverageSuite) TestChainIsEVM(c *C) {
	c.Assert(ETHChain.IsEVM(), Equals, true)
	c.Assert(AVAXChain.IsEVM(), Equals, true)
	c.Assert(BSCChain.IsEVM(), Equals, true)
	c.Assert(BASEChain.IsEVM(), Equals, true)
	c.Assert(POLChain.IsEVM(), Equals, true)
	c.Assert(BTCChain.IsEVM(), Equals, false)
}

func (s *CoverageSuite) TestChainIsUTXO(c *C) {
	c.Assert(BTCChain.IsUTXO(), Equals, true)
	c.Assert(LTCChain.IsUTXO(), Equals, true)
	c.Assert(BCHChain.IsUTXO(), Equals, true)
	c.Assert(DOGEChain.IsUTXO(), Equals, true)
	c.Assert(ZECChain.IsUTXO(), Equals, true)
	c.Assert(ETHChain.IsUTXO(), Equals, false)
}

func (s *CoverageSuite) TestGetEVMChains(c *C) {
	chains := GetEVMChains()
	c.Assert(len(chains) > 0, Equals, true)
}

func (s *CoverageSuite) TestGetUTXOChains(c *C) {
	chains := GetUTXOChains()
	c.Assert(len(chains) > 0, Equals, true)
}

func (s *CoverageSuite) TestChainGetSigningAlgo(c *C) {
	c.Assert(ETHChain.GetSigningAlgo(), Equals, SigningAlgoSecp256k1)
	c.Assert(BTCChain.GetSigningAlgo(), Equals, SigningAlgoSecp256k1)
	c.Assert(SOLChain.GetSigningAlgo(), Equals, SigningAlgoEd25519)
	c.Assert(SUIChain.GetSigningAlgo(), Equals, SigningAlgoEd25519)
	c.Assert(ADAChain.GetSigningAlgo(), Equals, SigningAlgoEd25519)
}

func (s *CoverageSuite) TestChainGetGasAsset(c *C) {
	c.Assert(BSCChain.GetGasAsset(), Equals, BNBBEP20Asset)
	c.Assert(AVAXChain.GetGasAsset(), Equals, AVAXAsset)
	c.Assert(BASEChain.GetGasAsset(), Equals, BaseETHAsset)
	c.Assert(TRONChain.GetGasAsset(), Equals, TRXAsset)
	c.Assert(XRPChain.GetGasAsset(), Equals, XRPAsset)
	c.Assert(SOLChain.GetGasAsset(), Equals, SOLAsset)
	c.Assert(ZECChain.GetGasAsset(), Equals, ZECAsset)
	c.Assert(POLChain.GetGasAsset(), Equals, POLAsset)
	c.Assert(SUIChain.GetGasAsset(), Equals, SUIAsset)
	c.Assert(ADAChain.GetGasAsset(), Equals, ADAAsset)
	c.Assert(THORChain.GetGasAsset(), Equals, RuneNative)
}

func (s *CoverageSuite) TestChainGetGasUnits(c *C) {
	units, perOne := XRPChain.GetGasUnits()
	c.Assert(units, Equals, "drop")
	c.Assert(perOne.Equal(cosmos.NewUint(1e6)), Equals, true)

	units, perOne = BTCChain.GetGasUnits()
	c.Assert(units, Equals, "satsperbyte")
	c.Assert(perOne.Equal(cosmos.NewUint(1e8)), Equals, true)

	units, perOne = ETHChain.GetGasUnits()
	c.Assert(units, Equals, "gwei")
	c.Assert(perOne.Equal(cosmos.NewUint(1e9)), Equals, true)

	units, _ = AVAXChain.GetGasUnits()
	c.Assert(units, Equals, "nAVAX")

	units, _ = BASEChain.GetGasUnits()
	c.Assert(units, Equals, "mwei")

	units, _ = SOLChain.GetGasUnits()
	c.Assert(units, Equals, "lamport")

	units, _ = SUIChain.GetGasUnits()
	c.Assert(units, Equals, "mist")

	units, _ = ADAChain.GetGasUnits()
	c.Assert(units, Equals, "lovelace")

	// default
	units, perOne = EmptyChain.GetGasUnits()
	c.Assert(units, Equals, "")
	c.Assert(perOne.Equal(cosmos.OneUint()), Equals, true)
}

func (s *CoverageSuite) TestChainNativeGasConversions(c *C) {
	// BTC: 1e8 per one, so 1e8 native = 1e8 thorchain
	result := BTCChain.NativeGasToThorchain(cosmos.NewUint(1e8))
	c.Assert(result.Equal(cosmos.NewUint(1e8)), Equals, true)

	result2 := BTCChain.ThorchainToNativeGas(cosmos.NewUint(1e8))
	c.Assert(result2.Equal(cosmos.NewUint(1e8)), Equals, true)

	// ETH: 1e9 per one, so 1e9 native = 1e8 thorchain
	result3 := ETHChain.NativeGasToThorchain(cosmos.NewUint(1e9))
	c.Assert(result3.Equal(cosmos.NewUint(1e8)), Equals, true)
}

func (s *CoverageSuite) TestChainGetGasAssetDecimal(c *C) {
	c.Assert(GAIAChain.GetGasAssetDecimal(), Equals, int64(6))
	c.Assert(NOBLEChain.GetGasAssetDecimal(), Equals, int64(6))
	c.Assert(TRONChain.GetGasAssetDecimal(), Equals, int64(6))
	c.Assert(XRPChain.GetGasAssetDecimal(), Equals, int64(6))
	c.Assert(SOLChain.GetGasAssetDecimal(), Equals, int64(9))
	c.Assert(SUIChain.GetGasAssetDecimal(), Equals, int64(9))
	c.Assert(ADAChain.GetGasAssetDecimal(), Equals, int64(6))
	c.Assert(ETHChain.GetGasAssetDecimal(), Equals, int64(8))
}

func (s *CoverageSuite) TestChainDustThreshold(c *C) {
	c.Assert(BTCChain.DustThreshold().Equal(cosmos.NewUint(1_000)), Equals, true)
	c.Assert(LTCChain.DustThreshold().Equal(cosmos.NewUint(100_000)), Equals, true)
	c.Assert(BCHChain.DustThreshold().Equal(cosmos.NewUint(10_000)), Equals, true)
	c.Assert(ZECChain.DustThreshold().Equal(cosmos.NewUint(15_000)), Equals, true)
	c.Assert(DOGEChain.DustThreshold().Equal(cosmos.NewUint(100_000_000)), Equals, true)
	c.Assert(ETHChain.DustThreshold().Equal(cosmos.NewUint(1_000)), Equals, true)
	c.Assert(AVAXChain.DustThreshold().Equal(cosmos.NewUint(100_000)), Equals, true)
	c.Assert(BSCChain.DustThreshold().Equal(cosmos.NewUint(10_000)), Equals, true)
	c.Assert(GAIAChain.DustThreshold().Equal(cosmos.NewUint(1_000_000)), Equals, true)
	c.Assert(XRPChain.DustThreshold().Equal(cosmos.NewUint(One)), Equals, true)
	c.Assert(SOLChain.DustThreshold().Equal(cosmos.NewUint(100_000)), Equals, true)
	c.Assert(TRONChain.DustThreshold().Equal(cosmos.NewUint(10_000_000)), Equals, true)
	c.Assert(SUIChain.DustThreshold().Equal(cosmos.NewUint(1_000_000)), Equals, true)
	c.Assert(ADAChain.DustThreshold().Equal(cosmos.NewUint(10_000_000)), Equals, true)
	c.Assert(EmptyChain.DustThreshold().Equal(cosmos.ZeroUint()), Equals, true)
}

func (s *CoverageSuite) TestChainP2WPKHOutputValue(c *C) {
	c.Assert(BTCChain.P2WPKHOutputValue(), Equals, int64(294))
	c.Assert(LTCChain.P2WPKHOutputValue(), Equals, int64(2940))
	c.Assert(DOGEChain.P2WPKHOutputValue(), Equals, int64(18_200_000))
	c.Assert(BCHChain.P2WPKHOutputValue(), Equals, int64(546))
	c.Assert(ZECChain.P2WPKHOutputValue(), Equals, int64(2730))
	c.Assert(ETHChain.P2WPKHOutputValue(), Equals, int64(0))
}

func (s *CoverageSuite) TestChainMaxMemoLength(c *C) {
	btcMax := BTCChain.MaxMemoLength()
	c.Assert(btcMax > 0, Equals, true)

	ethMax := ETHChain.MaxMemoLength()
	c.Assert(ethMax > 0, Equals, true)
}

func (s *CoverageSuite) TestChainDefaultCoinbase(c *C) {
	c.Assert(BTCChain.DefaultCoinbase(), Equals, 3.125)
	c.Assert(LTCChain.DefaultCoinbase(), Equals, 6.25)
	c.Assert(BCHChain.DefaultCoinbase(), Equals, 3.125)
	c.Assert(DOGEChain.DefaultCoinbase(), Equals, float64(10000))
	c.Assert(ZECChain.DefaultCoinbase(), Equals, 1.5625)
	c.Assert(ETHChain.DefaultCoinbase(), Equals, float64(0))
}

func (s *CoverageSuite) TestChainApproximateBlockMilliseconds(c *C) {
	c.Assert(BTCChain.ApproximateBlockMilliseconds(), Equals, int64(600_000))
	c.Assert(ETHChain.ApproximateBlockMilliseconds(), Equals, int64(12_000))
	c.Assert(SOLChain.ApproximateBlockMilliseconds(), Equals, int64(300))
	c.Assert(SUIChain.ApproximateBlockMilliseconds(), Equals, int64(250))
	c.Assert(ADAChain.ApproximateBlockMilliseconds(), Equals, int64(20_000))
	c.Assert(EmptyChain.ApproximateBlockMilliseconds(), Equals, int64(0))
}

func (s *CoverageSuite) TestChainInboundNotes(c *C) {
	c.Assert(len(BTCChain.InboundNotes()) > 0, Equals, true)
	c.Assert(len(ETHChain.InboundNotes()) > 0, Equals, true)
	c.Assert(len(THORChain.InboundNotes()) > 0, Equals, true)
	c.Assert(len(XRPChain.InboundNotes()) > 0, Equals, true)
	c.Assert(EmptyChain.InboundNotes(), Equals, "")
}

func (s *CoverageSuite) TestChainOutboundNotes(c *C) {
	c.Assert(len(XRPChain.OutboundNotes()) > 0, Equals, true)
	c.Assert(BTCChain.OutboundNotes(), Equals, "")
}

func (s *CoverageSuite) TestNewChains(c *C) {
	chains, err := NewChains([]string{"BTC", "ETH", "DOGE"})
	c.Assert(err, IsNil)
	c.Assert(chains, HasLen, 3)

	// with invalid
	chains2, err := NewChains([]string{"BTC", "x", "ETH"})
	c.Assert(err, NotNil)
	c.Assert(chains2, HasLen, 2)
}

func (s *CoverageSuite) TestChainsStrings(c *C) {
	chains := Chains{BTCChain, ETHChain}
	strs := chains.Strings()
	c.Assert(strs, HasLen, 2)
	c.Assert(strs[0], Equals, "BTC")
	c.Assert(strs[1], Equals, "ETH")
}

// --- Additional Median tests ---

func (s *CoverageSuite) TestGetMedianUint(c *C) {
	// empty
	c.Assert(GetMedianUint(nil).Equal(cosmos.ZeroUint()), Equals, true)

	// odd
	vals := []cosmos.Uint{cosmos.NewUint(3), cosmos.NewUint(1), cosmos.NewUint(2)}
	c.Assert(GetMedianUint(vals).Equal(cosmos.NewUint(2)), Equals, true)

	// even
	vals2 := []cosmos.Uint{cosmos.NewUint(1), cosmos.NewUint(2), cosmos.NewUint(3), cosmos.NewUint(4)}
	c.Assert(GetMedianUint(vals2).Equal(cosmos.NewUint(2)), Equals, true)
}

func (s *CoverageSuite) TestGetMedianInt64(c *C) {
	// empty
	c.Assert(GetMedianInt64(nil), Equals, int64(0))

	// single
	c.Assert(GetMedianInt64([]int64{5}), Equals, int64(5))

	// odd
	c.Assert(GetMedianInt64([]int64{3, 1, 2}), Equals, int64(2))

	// even
	c.Assert(GetMedianInt64([]int64{1, 2, 3, 4}), Equals, int64(2))
}

// --- ConvertDecimals tests ---

func (s *CoverageSuite) TestConvertDecimalsCoverage(c *C) {
	// from > to
	result, err := ConvertDecimals(big.NewInt(1000), 18, 8)
	c.Assert(err, IsNil)
	c.Assert(result.Int64() >= 0, Equals, true)

	// from < to
	result2, err := ConvertDecimals(big.NewInt(1000), 8, 18)
	c.Assert(err, IsNil)
	c.Assert(result2.Cmp(big.NewInt(0)) > 0, Equals, true)

	// equal decimals
	result3, err := ConvertDecimals(big.NewInt(1000), 8, 8)
	c.Assert(err, IsNil)
	c.Assert(result3.Int64(), Equals, int64(1000))

	// negative decimals
	_, err = ConvertDecimals(big.NewInt(1000), -1, 8)
	c.Assert(err, NotNil)
}

// --- Coin Valid tests ---

func (s *CoverageSuite) TestCoinValidExtra(c *C) {
	// empty asset
	coin := NewCoin(EmptyAsset, cosmos.NewUint(100))
	c.Assert(coin.Valid(), NotNil)

	// zero amount
	coin2 := NewCoin(BTCAsset, cosmos.ZeroUint())
	c.Assert(coin2.Valid(), NotNil)
}

// --- Coin Equals ---

func (s *CoverageSuite) TestCoinEquals(c *C) {
	c1 := NewCoin(BTCAsset, cosmos.NewUint(100))
	c2 := NewCoin(BTCAsset, cosmos.NewUint(100))
	c.Assert(c1.Equals(c2), Equals, true)

	c3 := NewCoin(ETHAsset, cosmos.NewUint(100))
	c.Assert(c1.Equals(c3), Equals, false)

	c4 := NewCoin(BTCAsset, cosmos.NewUint(200))
	c.Assert(c1.Equals(c4), Equals, false)
}

// --- LatestVersion test ---

func (s *CoverageSuite) TestLatestVersion(c *C) {
	c.Assert(LatestVersion.String(), Equals, "999.0.0")
}

// --- Chain IsValidAddress test ---

func (s *CoverageSuite) TestChainIsValidAddress(c *C) {
	// EVM addresses should start with 0x
	c.Assert(ETHChain.IsValidAddress(Address("0x90f2b1ae50e6018230e90a33f98c7844a0ab635a")), Equals, true)
	c.Assert(ETHChain.IsValidAddress(Address("invalid")), Equals, false)
}

// --- GetMedianBigFloat test ---

func (s *CoverageSuite) TestGetMedianBigFloat(c *C) {
	// empty
	_, err := GetMedianBigFloat(nil)
	c.Assert(err, NotNil)

	// single
	result, err := GetMedianBigFloat([]*big.Float{big.NewFloat(5.0)})
	c.Assert(err, IsNil)
	c.Assert(result.Text('f', 1), Equals, "5.0")

	// odd
	result2, err := GetMedianBigFloat([]*big.Float{big.NewFloat(3.0), big.NewFloat(1.0), big.NewFloat(2.0)})
	c.Assert(err, IsNil)
	c.Assert(result2.Text('f', 1), Equals, "2.0")

	// even
	result3, err := GetMedianBigFloat([]*big.Float{big.NewFloat(1.0), big.NewFloat(2.0), big.NewFloat(3.0), big.NewFloat(4.0)})
	c.Assert(err, IsNil)
	c.Assert(result3.Text('f', 1), Equals, "2.5")
}
