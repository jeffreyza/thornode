package solana

import (
	"encoding/binary"
	"time"

	"github.com/mr-tron/base58"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	soltypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

type NonceAdoptSuite struct{}

var _ = Suite(&NonceAdoptSuite{})

func adoptKeys(c *C) (vault, nonce soltypes.PublicKey, vaultStr, nonceStr string) {
	vaultStr, nonceStr = attestFixture(c)
	return soltypes.MustPublicKeyFromString(vaultStr), soltypes.MustPublicKeyFromString(nonceStr), vaultStr, nonceStr
}

func discs(tx soltypes.Transaction) []uint32 {
	out := make([]uint32, 0, len(tx.Message.Instructions))
	for _, ins := range tx.Message.Instructions {
		out = append(out, binary.LittleEndian.Uint32(ins.Data[:4]))
	}
	return out
}

// The nonce address is public, so anyone can pre-fund it (CreateAccountWithSeed then
// fails forever). A plain System account is adopted in place; any other state is not
// provisioned (no doomed keysign).
func (s *NonceAdoptSuite) TestProvisionTxChoice(c *C) {
	vault, nonce, _, _ := adoptKeys(c)
	const bh = "4sGjMW1sUnHzSxGspuhpqLDx6wiyjNtZAMdL4VZHirAn"
	system := soltypes.SystemProgramID.String()

	tx, ok := nonceProvisionTx(rpc.AccountMeta{}, vault, nonce, bh)
	c.Assert(ok, Equals, true)
	c.Assert(discs(tx), DeepEquals, []uint32{solSysCreateAccountWithSeed, solSysInitializeNonce})

	tx, ok = nonceProvisionTx(rpc.AccountMeta{Exists: true, Owner: system, Lamports: 1}, vault, nonce, bh)
	c.Assert(ok, Equals, true)
	c.Assert(discs(tx), DeepEquals, []uint32{solSysAllocateWithSeed, solSysTransfer, solSysInitializeNonce})

	// The adopt bytes do not depend on how much was pre-funded (all members agree).
	other, _ := nonceProvisionTx(rpc.AccountMeta{Exists: true, Owner: system, Lamports: 777_777}, vault, nonce, bh)
	a, err := tx.Serialize()
	c.Assert(err, IsNil)
	b, err := other.Serialize()
	c.Assert(err, IsNil)
	c.Assert(a, DeepEquals, b)

	for name, meta := range map[string]rpc.AccountMeta{
		"other owner": {Exists: true, Owner: "TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA", Lamports: 1},
		"has data":    {Exists: true, Owner: system, Lamports: 1, Space: 80},
		"executable":  {Exists: true, Owner: system, Lamports: 1, Executable: true},
	} {
		_, ok := nonceProvisionTx(meta, vault, nonce, bh)
		c.Check(ok, Equals, false, Commentf(name))
	}
}

// AllocateWithSeed: disc 9 | base | seed | space 80 | owner System; accounts
// [nonce, vault]; the transfer funds the nonce account with the full rent.
func (s *NonceAdoptSuite) TestAdoptTxLayout(c *C) {
	vault, nonce, _, _ := adoptKeys(c)
	tx := soltypes.NewAdoptNonceAccountTransaction(vault, nonce, solNonceSeed, nonceAccountRentLamports, "4sGjMW1sUnHzSxGspuhpqLDx6wiyjNtZAMdL4VZHirAn")
	c.Assert(tx.Message.Accounts[0], Equals, vault)
	c.Assert(tx.Message.Accounts[1], Equals, nonce)
	alloc := tx.Message.Instructions[0]
	c.Assert(alloc.Accounts, DeepEquals, []int{1, 0})
	d := alloc.Data
	c.Assert(soltypes.PublicKey(d[4:36]), Equals, vault)
	seedLen := int(binary.LittleEndian.Uint64(d[36:44]))
	c.Assert(string(d[44:44+seedLen]), Equals, solNonceSeed)
	c.Assert(binary.LittleEndian.Uint64(d[44+seedLen:52+seedLen]), Equals, uint64(soltypes.NonceAccountSize))
	c.Assert(soltypes.PublicKey(d[52+seedLen:84+seedLen]), Equals, soltypes.SystemProgramID)
	transfer := tx.Message.Instructions[1]
	c.Assert(transfer.Accounts, DeepEquals, []int{0, 1})
	c.Assert(binary.LittleEndian.Uint64(transfer.Data[4:12]), Equals, nonceAccountRentLamports)
	c.Assert(tx.Message.Instructions[2].Accounts[0], Equals, 1)
}

// toRPCTx renders a built transaction the way getTransaction returns it.
func toRPCTx(tx soltypes.Transaction, fee uint64) *rpc.TransactionResult {
	keys := make([]string, 0, len(tx.Message.Accounts))
	for _, k := range tx.Message.Accounts {
		keys = append(keys, k.String())
	}
	ins := make([]rpc.RPCInstruction, 0, len(tx.Message.Instructions))
	for _, in := range tx.Message.Instructions {
		ins = append(ins, rpc.RPCInstruction{ProgramIdIndex: in.ProgramIDIndex, Accounts: in.Accounts, Data: base58.Encode(in.Data)})
	}
	return &rpc.TransactionResult{
		Slot: 100,
		Meta: rpc.RPCMeta{Fee: fee},
		Transaction: rpc.RPCTxnData{
			Signatures: []string{testTxSig},
			Message:    rpc.RPCMessage{AccountKeys: keys, Instructions: ins},
		},
	}
}

// The scanner observes the adopt shape like a creation: the transfer's rent to the
// nonce account, the fee as gas, and the attested initial nonce.
func (s *NonceAdoptSuite) TestScannerObservesAdoption(c *C) {
	vault, nonce, vaultStr, nonceStr := adoptKeys(c)
	n := &nonceRPC{authority: vaultStr}
	srv := n.server()
	defer srv.Close()
	scanner := newTestScanner()
	scanner.solRpc = rpc.NewSolRPC(srv.URL, time.Second)

	adopt := toRPCTx(soltypes.NewAdoptNonceAccountTransaction(vault, nonce, solNonceSeed, nonceAccountRentLamports, "4sGjMW1sUnHzSxGspuhpqLDx6wiyjNtZAMdL4VZHirAn"), 5000)
	item := scanner.getTxInItem(adopt, 100)
	c.Assert(item, NotNil)
	c.Assert(item.Memo, Equals, "")
	c.Assert(item.Sender, Equals, vaultStr)
	c.Assert(item.To, Equals, nonceStr)
	c.Assert(item.Coins[0].Amount.Uint64(), Equals, convertLamportsToTHORChain(nonceAccountRentLamports).Uint64())
	c.Assert(item.SolNonce, NotNil)
	c.Assert(item.SolNonce.NonceAccount, Equals, nonceStr)
	_ = common.SOLChain
}
