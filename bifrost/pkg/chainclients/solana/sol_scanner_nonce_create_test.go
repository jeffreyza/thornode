package solana

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/mr-tron/base58"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	soltypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

type ScannerNonceCreateSuite struct{}

var _ = Suite(&ScannerNonceCreateSuite{})

const nonceRentLamports = uint64(1_447_680)

// createWithSeedData encodes SystemProgram CreateAccountWithSeed.
func createWithSeedData(base string, lamports uint64) string {
	d := make([]byte, 4, 128)
	binary.LittleEndian.PutUint32(d, solSysCreateAccountWithSeed)
	b, _ := base58.Decode(base)
	d = append(d, b...)
	d = binary.LittleEndian.AppendUint64(d, uint64(len(common.SolDurableNonceSeed)))
	d = append(d, common.SolDurableNonceSeed...)
	d = binary.LittleEndian.AppendUint64(d, lamports)
	d = binary.LittleEndian.AppendUint64(d, 80)
	d = append(d, soltypes.SystemProgramID.Bytes()...)
	return base58.Encode(d)
}

// nonceCreateTx is the vault funding (and initializing) created as its nonce account.
func nonceCreateTx(vault, created string) *rpc.TransactionResult {
	return &rpc.TransactionResult{
		Slot: 100,
		Meta: rpc.RPCMeta{Fee: 5000},
		Transaction: rpc.RPCTxnData{
			Signatures: []string{testTxSig},
			Message: rpc.RPCMessage{
				AccountKeys: []string{vault, created, testSystemProgram, soltypes.SysVarRecentBlockhashesID.String(), soltypes.SysVarRentID.String()},
				Instructions: []rpc.RPCInstruction{
					{ProgramIdIndex: 2, Accounts: []int{0, 1, 0}, Data: createWithSeedData(vault, nonceRentLamports)},
					{ProgramIdIndex: 2, Accounts: []int{1, 3, 4}, Data: u32Data(solSysInitializeNonce)},
				},
			},
		},
	}
}

func (s *ScannerNonceCreateSuite) scanner(n *nonceRPC) (*SOLScanner, func()) {
	srv := n.server()
	scanner := newTestScanner()
	scanner.solRpc = rpc.NewSolRPC(srv.URL, time.Second)
	return scanner, srv.Close
}

// The vault's own nonce-account creation is observed as a memoless outflow of the
// rent to the nonce account, with the fee as gas and the initial nonce attested.
func (s *ScannerNonceCreateSuite) TestObservesOwnNonceCreation(c *C) {
	vault, nonceAcct := attestFixture(c)
	scanner, done := s.scanner(&nonceRPC{authority: vault})
	defer done()

	item := scanner.getTxInItem(nonceCreateTx(vault, nonceAcct), 100)
	c.Assert(item, NotNil)
	c.Assert(item.Memo, Equals, "")
	c.Assert(item.Sender, Equals, vault)
	c.Assert(item.To, Equals, nonceAcct)
	c.Assert(item.Coins, HasLen, 1)
	c.Assert(item.Coins[0].Asset.Equals(common.SOLAsset), Equals, true)
	c.Assert(item.Coins[0].Amount.Uint64(), Equals, convertLamportsToTHORChain(nonceRentLamports).Uint64())
	c.Assert(item.Gas, HasLen, 1)
	c.Assert(item.Gas[0].Amount.Uint64(), Equals, convertLamportsToTHORChain(5000).Uint64())
	c.Assert(item.SolNonce, NotNil)
	c.Assert(item.SolNonce.NonceAccount, Equals, nonceAcct)
	c.Assert(item.SolNonce.Nonce, Equals, attestedNonce)
}

// Without an attestable nonce the creation is NOT observed at all: a foreign account,
// a foreign authority, an unreadable account, or a second create in the same tx.
func (s *ScannerNonceCreateSuite) TestNotObservedWithoutAttestedNonce(c *C) {
	vault, nonceAcct := attestFixture(c)
	foreign, _ := attestFixture(c)

	scanner, done := s.scanner(&nonceRPC{authority: vault})
	defer done()
	c.Assert(scanner.getTxInItem(nonceCreateTx(vault, foreign), 100), IsNil) // not derived from the funder

	scanner2, done2 := s.scanner(&nonceRPC{authority: foreign})
	defer done2()
	c.Assert(scanner2.getTxInItem(nonceCreateTx(vault, nonceAcct), 100), IsNil) // authority is not the vault

	scanner3, done3 := s.scanner(&nonceRPC{authority: vault, fail: true})
	defer done3()
	c.Assert(scanner3.getTxInItem(nonceCreateTx(vault, nonceAcct), 100), IsNil) // unreadable

	twice := nonceCreateTx(vault, nonceAcct)
	ins := twice.Transaction.Message.Instructions
	twice.Transaction.Message.Instructions = append([]rpc.RPCInstruction{ins[0]}, ins...)
	c.Assert(scanner.getTxInItem(twice, 100), IsNil) // a second create's rent would go unbooked
}

// scanRPC serves one vault signature whose tx is tx, and the nonce account (failing
// while fail is set).
type scanRPC struct {
	nonceRPC
	tx *rpc.TransactionResult
}

func (n *scanRPC) server(c *C) *httptest.Server {
	inner := n.nonceRPC.server()
	txJSON, err := json.Marshal(n.tx)
	c.Assert(err, IsNil)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "getSignaturesForAddress":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":[{"signature":%q,"slot":100}]}`, testTxSig)
		case "getTransaction":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, txJSON)
		default: // getAccountInfo and anything else: the nonce server
			resp, err := http.Post(inner.URL, "application/json", bytes.NewReader(body))
			if err != nil {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(w, resp.Body)
		}
	}))
}

// A transient RPC failure reading the created account's nonce fails the vault scan
// (cursor held, nothing queued) instead of dropping the creation; the next scan
// re-reads the tx and observes it.
func (s *ScannerNonceCreateSuite) TestNonceReadErrorRetriesScan(c *C) {
	vault, nonceAcct := attestFixture(c)
	n := &scanRPC{nonceRPC: nonceRPC{authority: vault, fail: true}, tx: nonceCreateTx(vault, nonceAcct)}
	srv := n.server(c)
	defer srv.Close()
	scanner := newTestScanner()
	scanner.cfg.ChainID = common.SOLChain
	scanner.solRpc = rpc.NewSolRPC(srv.URL, time.Second)
	scanner.globalTxsQueue = make(chan stypes.TxIn, 10)

	scan := func() (ok, healthy bool, cursor string) {
		var wg sync.WaitGroup
		wg.Add(1)
		okQ, counts, sigs := make([]bool, 1), make([]int, 1), make([]string, 1)
		healthy = true
		scanner.scanVault(vault, 0, "", 0, func() { healthy = false }, counts, okQ, sigs, &wg)
		wg.Wait()
		return okQ[0], healthy, sigs[0]
	}

	ok, healthy, cursor := scan()
	c.Assert(ok, Equals, false)
	c.Assert(healthy, Equals, false)
	c.Assert(cursor, Equals, "")
	c.Assert(scanner.globalTxsQueue, HasLen, 0)

	n.fail = false
	ok, healthy, cursor = scan()
	c.Assert(ok, Equals, true)
	c.Assert(healthy, Equals, true)
	c.Assert(cursor, Equals, testTxSig)
	c.Assert(scanner.globalTxsQueue, HasLen, 1)
	txIn := <-scanner.globalTxsQueue
	c.Assert(txIn.TxArray, HasLen, 1)
	c.Assert(txIn.TxArray[0].SolNonce, NotNil)
	c.Assert(txIn.TxArray[0].SolNonce.Nonce, Equals, attestedNonce)
}
