package solana

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/mr-tron/base58"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	soltypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/common"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type ScannerNonceAttestSuite struct{}

var _ = Suite(&ScannerNonceAttestSuite{})

const attestedNonce = "9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo"

// nonceRPC serves getAccountInfo for a nonce account (authority/value configurable,
// or an RPC error) and counts the reads.
type nonceRPC struct {
	authority string
	fail      bool
	calls     atomic.Int32
}

func (n *nonceRPC) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method != "getAccountInfo" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
			return
		}
		n.calls.Add(1)
		if n.fail {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"node is behind"}}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":101},"value":{"owner":"11111111111111111111111111111111","data":{"program":"nonce","parsed":{"type":"initialized","info":{"authority":%q,"blockhash":%q}}}}}}`, n.authority, attestedNonce)
	}))
}

func u32Data(v uint32) string {
	d := make([]byte, 4)
	binary.LittleEndian.PutUint32(d, v)
	return base58.Encode(d)
}

// durableOutboundTx is a vault's durable-nonce outbound: AdvanceNonceAccount on
// advanced (instruction 0), a memo, and a transfer to dest.
func durableOutboundTx(vault, dest, advanced string) *rpc.TransactionResult {
	return &rpc.TransactionResult{
		Slot: 100,
		Meta: rpc.RPCMeta{Fee: 5000},
		Transaction: rpc.RPCTxnData{
			Signatures: []string{testTxSig},
			Message: rpc.RPCMessage{
				AccountKeys: []string{vault, dest, advanced, testSystemProgram, soltypes.SysVarRecentBlockhashesID.String(), testMemoProgram},
				Instructions: []rpc.RPCInstruction{
					{ProgramIdIndex: 3, Accounts: []int{2, 4, 0}, Data: u32Data(solSysAdvanceNonce)},
					{ProgramIdIndex: 5, Accounts: []int{0}, Data: makeMemoData("OUT:ABCDEF")},
					{ProgramIdIndex: 3, Accounts: []int{0, 1}, Data: makeTransferData(1_000_000_000)},
				},
			},
		},
	}
}

func attestFixture(c *C) (vault, nonceAcct string) {
	addr, err := types2.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	nonceAcct, err = common.SolNonceAccountAddress(addr.String())
	c.Assert(err, IsNil)
	return addr.String(), nonceAcct
}

// TestAttestsSendersOwnAdvancedNonce: an observed durable outbound that advanced the
// sender vault's own seed-derived nonce account carries the post-tx nonce, read at
// the tx slot, for THORChain to commit.
func (s *ScannerNonceAttestSuite) TestAttestsSendersOwnAdvancedNonce(c *C) {
	vault, nonceAcct := attestFixture(c)
	n := &nonceRPC{authority: vault}
	srv := n.server()
	defer srv.Close()
	scanner := newTestScanner()
	scanner.solRpc = rpc.NewSolRPC(srv.URL, time.Second)

	item := scanner.getTxInItem(durableOutboundTx(vault, testVault, nonceAcct), 100)
	c.Assert(item, NotNil)
	c.Assert(item.Memo, Equals, "OUT:ABCDEF") // still a normal outbound observation
	c.Assert(item.SolNonce, NotNil)
	c.Assert(item.SolNonce.NonceAccount, Equals, nonceAcct)
	c.Assert(item.SolNonce.Nonce, Equals, attestedNonce)
	c.Assert(item.SolNonce.Slot, Equals, int64(100))
	c.Assert(n.calls.Load(), Equals, int32(1))
}

// TestNoAttestationOtherwise: nothing is attested (and the account is never read)
// for a foreign nonce account or a tx without AdvanceNonceAccount; a failed read or
// a non-vault authority attests nothing but still returns the observation.
func (s *ScannerNonceAttestSuite) TestNoAttestationOtherwise(c *C) {
	vault, nonceAcct := attestFixture(c)

	// Foreign nonce account (not derived from the sender): no read, no attestation.
	foreign, _ := attestFixture(c)
	n := &nonceRPC{authority: vault}
	srv := n.server()
	defer srv.Close()
	scanner := newTestScanner()
	scanner.solRpc = rpc.NewSolRPC(srv.URL, time.Second)
	item := scanner.getTxInItem(durableOutboundTx(vault, testVault, foreign), 100)
	c.Assert(item, NotNil)
	c.Assert(item.SolNonce, IsNil)
	c.Assert(n.calls.Load(), Equals, int32(0))

	// A plain transfer (no AdvanceNonceAccount): no read.
	plain := durableOutboundTx(vault, testVault, nonceAcct)
	plain.Transaction.Message.Instructions = plain.Transaction.Message.Instructions[1:]
	item = scanner.getTxInItem(plain, 100)
	c.Assert(item, NotNil)
	c.Assert(item.SolNonce, IsNil)
	c.Assert(n.calls.Load(), Equals, int32(0))

	// RPC error: observation still returned, without a nonce.
	failing := &nonceRPC{authority: vault, fail: true}
	fsrv := failing.server()
	defer fsrv.Close()
	scanner.solRpc = rpc.NewSolRPC(fsrv.URL, time.Second)
	item = scanner.getTxInItem(durableOutboundTx(vault, testVault, nonceAcct), 100)
	c.Assert(item, NotNil)
	c.Assert(item.SolNonce, IsNil)

	// Authority is not the sender: not attested.
	other, _ := attestFixture(c)
	wrong := &nonceRPC{authority: other}
	wsrv := wrong.server()
	defer wsrv.Close()
	scanner.solRpc = rpc.NewSolRPC(wsrv.URL, time.Second)
	item = scanner.getTxInItem(durableOutboundTx(vault, testVault, nonceAcct), 100)
	c.Assert(item, NotNil)
	c.Assert(item.SolNonce, IsNil)

	// No RPC client configured: no panic, no attestation.
	scanner.solRpc = nil
	item = scanner.getTxInItem(durableOutboundTx(vault, testVault, nonceAcct), 100)
	c.Assert(item, NotNil)
	c.Assert(item.SolNonce, IsNil)
}

// TestNonceAddressDerivationMatchesCommon: THORChain re-derives the nonce account in
// common (commit-time check) and bifrost derives it in the solana types package
// (provisioning, signing); they must agree byte-for-byte.
func (s *ScannerNonceAttestSuite) TestNonceAddressDerivationMatchesCommon(c *C) {
	cl := &SOLClient{}
	for i := 0; i < 20; i++ {
		addr, err := types2.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
		c.Assert(err, IsNil)
		pk, err := soltypes.PublicKeyFromString(addr.String())
		c.Assert(err, IsNil)
		fromTypes, err := cl.nonceAddressForVault(pk)
		c.Assert(err, IsNil)
		fromCommon, err := common.SolNonceAccountAddress(addr.String())
		c.Assert(err, IsNil)
		c.Assert(fromCommon, Equals, fromTypes.String())
	}
	c.Assert(solNonceSeed, Equals, common.SolDurableNonceSeed)
}
