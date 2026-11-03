package solana

import (
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type ReattestSuite struct{}

var _ = Suite(&ReattestSuite{})

// fakeNonceChain serves the nonce account, its signatures (newest first) and the
// txs behind them.
type fakeNonceChain struct {
	nonce, authority string
	nonceErr         error
	sigs             []rpc.SignaturesForAddressResult
	sigErr           error
	until            string
	signers          map[string]string // signature -> first required signer ("" = unreadable)
}

func (f *fakeNonceChain) GetNonceAccount(string, uint64) (string, string, error) {
	return f.nonce, f.authority, f.nonceErr
}

func (f *fakeNonceChain) GetSignaturesForAddressUntil(_ string, p rpc.GetSignaturesForAddressUntilParams) ([]rpc.SignaturesForAddressResult, error) {
	f.until = p.Until
	return f.sigs, f.sigErr
}

func (f *fakeNonceChain) GetTransaction(sig string) (*rpc.TransactionResult, error) {
	signer, ok := f.signers[sig]
	if !ok || signer == "" {
		return nil, errors.New("not found")
	}
	if signer == nullTxResult {
		return &rpc.TransactionResult{}, nil // RPC answered "result": null
	}
	tx := &rpc.TransactionResult{Slot: 1}
	tx.Transaction.Message.AccountKeys = []string{signer, "other"}
	tx.Transaction.Message.Header.NumRequiredSignatures = 1
	return tx, nil
}

// nullTxResult marks a signature whose getTransaction answers "result": null
// (pruned, unknown or lagging backend): decoded as an empty result with no error.
const nullTxResult = "<null>"

type reattestFixture struct {
	vault     string
	committed thorclient.SolVaultNonce
	chain     *fakeNonceChain
	observed  observedTxChecker
}

// fakeObserved reports THORChain observations: finalised[txID], or an error.
type fakeObserved struct {
	finalised map[string]bool
	err       error
}

func (f *fakeObserved) GetObservedTxVoterUncached(txID string) (*types2.QueryObservedTxVoter, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	fin, ok := f.finalised[txID]
	if !ok {
		return nil, false, nil
	}
	v := &types2.QueryObservedTxVoter{}
	if fin {
		v.FinalisedHeight = 100
	}
	return v, true, nil
}

const reattestHeight = int64(10_000)

var failed = map[string]any{"InstructionError": []any{1, "x"}}

func newReattestFixture(c *C) reattestFixture {
	addr, err := types2.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	acct, err := common.SolNonceAccountAddress(addr.String())
	c.Assert(err, IsNil)
	committed := thorclient.SolVaultNonce{
		NonceAccount: acct, Nonce: "committedNonce", Slot: 100,
		UpdatedHeight: reattestHeight - solNonceReattestMinAgeBlocks, SetByTx: "setBySig",
	}
	return reattestFixture{
		vault: addr.String(), committed: committed,
		chain: &fakeNonceChain{
			nonce: "advancedNonce", authority: addr.String(),
			sigs: []rpc.SignaturesForAddressResult{
				{Signature: "dust", Slot: 130},                 // someone else's successful transfer
				{Signature: "failed2", Slot: 120, Err: failed}, // newest failed vault tx
				{Signature: "failed1", Slot: 110, Err: failed},
			},
			signers: map[string]string{"dust": "stranger", "failed2": addr.String(), "failed1": addr.String()},
		},
	}
}

func (f reattestFixture) decide() (solNonceReattestation, bool) {
	return solNonceReattestationFor(f.chain, f.observed, log.Logger, f.vault, f.committed, reattestHeight)
}

// Failed vault txs moved the nonce: attest the on-chain value, bounded by set_by_tx,
// with the newest failed vault tx as last_tx (a stranger's tx is ignored).
func (s *ReattestSuite) TestAttestsWhenOnlyFailedVaultTxs(c *C) {
	f := newReattestFixture(c)
	att, ok := f.decide()
	c.Assert(ok, Equals, true)
	c.Assert(att.nonce, Equals, "advancedNonce")
	c.Assert(att.nonceAccount, Equals, f.committed.NonceAccount)
	c.Assert(att.lastTx, Equals, common.TxID("failed2"))
	c.Assert(att.slot, Equals, int64(120))
	c.Assert(f.chain.until, Equals, "setBySig")
}

func (s *ReattestSuite) TestDoesNotAttest(c *C) {
	cases := map[string]func(f *reattestFixture){
		"too young":          func(f *reattestFixture) { f.committed.UpdatedHeight++ },
		"no set_by_tx bound": func(f *reattestFixture) { f.committed.SetByTx = "" },
		"not the derived account": func(f *reattestFixture) {
			f.committed.NonceAccount = "someOtherAccount"
		},
		"nonce unchanged":       func(f *reattestFixture) { f.chain.nonce = f.committed.Nonce },
		"foreign authority":     func(f *reattestFixture) { f.chain.authority = "stranger" },
		"nonce read error":      func(f *reattestFixture) { f.chain.nonceErr = errors.New("rpc") },
		"signatures read error": func(f *reattestFixture) { f.chain.sigErr = errors.New("rpc") },
		"no signatures":         func(f *reattestFixture) { f.chain.sigs = nil },
		"too many signatures": func(f *reattestFixture) {
			for i := 0; i < 100; i++ {
				f.chain.sigs = append(f.chain.sigs, rpc.SignaturesForAddressResult{Signature: fmt.Sprint(i), Err: failed})
				f.chain.signers[fmt.Sprint(i)] = f.vault
			}
		},
		"successful vault tx (possible unobserved payment)": func(f *reattestFixture) {
			f.chain.sigs = append([]rpc.SignaturesForAddressResult{{Signature: "paid", Slot: 140}}, f.chain.sigs...)
			f.chain.signers["paid"] = f.vault
		},
		"unreadable tx": func(f *reattestFixture) { f.chain.signers["failed1"] = "" },
		"successful tx whose getTransaction is null (landed payment not visible)": func(f *reattestFixture) {
			f.chain.sigs = append([]rpc.SignaturesForAddressResult{{Signature: "paid", Slot: 140}}, f.chain.sigs...)
			f.chain.signers["paid"] = nullTxResult
		},
		"only strangers' txs": func(f *reattestFixture) {
			f.chain.signers["failed1"], f.chain.signers["failed2"] = "stranger", "stranger"
		},
	}
	for name, mutate := range cases {
		f := newReattestFixture(c)
		mutate(&f)
		_, ok := f.decide()
		c.Check(ok, Equals, false, Commentf(name))
	}
}

// reattestBridge serves asgards and committed nonces, and records attestations.
type reattestBridge struct {
	*mockSOLThorchainBridge
	vaults    types2.Vaults
	committed map[string]thorclient.SolVaultNonce
	posted    []string
	mimirErr  error
}

func (b *reattestBridge) GetAsgards() (types2.Vaults, error) { return b.vaults, nil }

func (b *reattestBridge) GetSolVaultNonce(pk string, _ int64) (thorclient.SolVaultNonce, error) {
	return b.committed[pk], nil
}

func (b *reattestBridge) PostSolNonceAttest(pk common.PubKey, _, nonce string, _ int64, lastTx common.TxID) (common.TxID, error) {
	b.posted = append(b.posted, fmt.Sprintf("%s|%s|%s", pk, nonce, lastTx))
	return "", nil
}

// Every active validator attests each wedged vault, member or not.
func (s *ReattestSuite) TestReattestVaultNoncesPosts(c *C) {
	eddsa := types2.GetRandomEd25519PubKey()
	addr, err := eddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	f := newReattestFixture(c)
	acct, err := common.SolNonceAccountAddress(addr.String())
	c.Assert(err, IsNil)
	f.committed.NonceAccount = acct
	f.chain.authority = addr.String()
	for k, v := range f.chain.signers {
		if v == f.vault {
			f.chain.signers[k] = addr.String()
		}
	}
	vault := types2.Vault{PubKey: types2.GetRandomPubKey(), PubKeyEddsa: eddsa} // this node is not a member
	idle := types2.Vault{PubKey: types2.GetRandomPubKey(), PubKeyEddsa: types2.GetRandomEd25519PubKey()}
	bridge := &reattestBridge{
		mockSOLThorchainBridge: &mockSOLThorchainBridge{},
		vaults:                 types2.Vaults{vault, idle},
		committed:              map[string]thorclient.SolVaultNonce{vault.PubKey.String(): f.committed},
	}
	cl := &SOLClient{bridge: bridge, nonceReader: f.chain, logger: log.Logger}
	cl.reattestVaultNonces(reattestHeight)
	c.Assert(bridge.posted, DeepEquals, []string{fmt.Sprintf("%s|advancedNonce|failed2", vault.PubKey)})
}

func (b *reattestBridge) FetchNodeStatus() (types2.NodeStatus, error) {
	return types2.NodeStatus_Active, nil
}

func (b *reattestBridge) GetMimir(string) (int64, error) { return 0, b.mimirErr }

// The provisioning cadence runs re-attestation for an active validator.
func (s *ReattestSuite) TestProvisionCadenceRunsReattest(c *C) {
	eddsa := types2.GetRandomEd25519PubKey()
	addr, err := eddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	acct, err := common.SolNonceAccountAddress(addr.String())
	c.Assert(err, IsNil)
	f := newReattestFixture(c)
	f.committed.NonceAccount = acct
	f.chain.authority = addr.String()
	f.chain.signers["failed2"], f.chain.signers["failed1"] = addr.String(), addr.String()
	vault := types2.Vault{PubKey: types2.GetRandomPubKey(), PubKeyEddsa: eddsa}
	bridge := &reattestBridge{
		mockSOLThorchainBridge: &mockSOLThorchainBridge{},
		vaults:                 types2.Vaults{vault},
		committed:              map[string]thorclient.SolVaultNonce{vault.PubKey.String(): f.committed},
		// Re-attestation must not depend on the provisioning mimir (vaults already
		// durable need recovery whatever it says), even when it cannot be read.
		mimirErr: errors.New("mimir unavailable"),
	}
	// An unhealthy scanner stops provisioning after re-attestation has run.
	cl := &SOLClient{bridge: bridge, nonceReader: f.chain, logger: log.Logger, solScanner: &SOLScanner{healthy: &atomic.Bool{}}}
	cl.nonceProvisionCheck(reattestHeight)
	c.Assert(bridge.posted, HasLen, 1)
}

// A successful vault tx that THORChain has a finalised observation of (its nonce votes
// split, so it never committed) is covered: re-attest past it. Unobserved, or
// unreadable, still blocks.
func (s *ReattestSuite) TestObservedSuccessfulVaultTx(c *C) {
	withPaid := func(obs observedTxChecker) (solNonceReattestation, bool) {
		f := newReattestFixture(c)
		f.chain.sigs = append([]rpc.SignaturesForAddressResult{{Signature: "paid", Slot: 140}}, f.chain.sigs...)
		f.chain.signers["paid"] = f.vault
		f.observed = obs
		return f.decide()
	}
	att, ok := withPaid(&fakeObserved{finalised: map[string]bool{"paid": true}})
	c.Assert(ok, Equals, true)
	c.Assert(att.lastTx, Equals, common.TxID("paid")) // newest vault tx
	c.Assert(att.slot, Equals, int64(140))

	_, ok = withPaid(&fakeObserved{finalised: map[string]bool{"paid": false}}) // observed, not final
	c.Check(ok, Equals, false)
	_, ok = withPaid(&fakeObserved{finalised: map[string]bool{}}) // not observed
	c.Check(ok, Equals, false)
	_, ok = withPaid(&fakeObserved{err: errors.New("rpc")}) // unknown
	c.Check(ok, Equals, false)
	_, ok = withPaid(nil) // no checker
	c.Check(ok, Equals, false)
}
