package solana

import (
	"encoding/binary"
	"testing"

	"github.com/mr-tron/base58"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
)

// TestIsDurableNonceCreateTx verifies the strict creation classifier: a bare
// durable-nonce account creation is matched (observed as a creation, see
// nonceCreateTxInItem), while any real outbound — including one bundled with a nonce
// create — is NOT matched, so it is always observed as a normal outbound.
func TestIsDurableNonceCreateTx(t *testing.T) {
	const (
		idxVault  = 0
		idxNonce  = 1
		idxSystem = 2
		idxMemo   = 3
		idxRecip  = 4
	)
	accounts := []string{
		"VaultAddr11111111111111111111111111111111",
		"NonceAddr11111111111111111111111111111111",
		types.SystemProgramID.String(),
		types.MemoProgramID.String(),
		"RecipAddr11111111111111111111111111111111",
	}
	acctBy := func(i int) string {
		if i < 0 || i >= len(accounts) {
			return ""
		}
		return accounts[i]
	}
	// data with a given u32-LE discriminator (only the first 4 bytes matter to the filter).
	data := func(disc uint32) string {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, disc)
		return base58.Encode(b)
	}
	createWithSeed := func(createdIdx int) rpc.RPCInstruction {
		return rpc.RPCInstruction{ProgramIdIndex: idxSystem, Accounts: []int{idxVault, createdIdx, idxVault}, Data: data(solSysCreateAccountWithSeed)}
	}
	createAccount := func(createdIdx int) rpc.RPCInstruction {
		return rpc.RPCInstruction{ProgramIdIndex: idxSystem, Accounts: []int{idxVault, createdIdx}, Data: data(solSysCreateAccount)}
	}
	initNonce := func(nonceIdx int) rpc.RPCInstruction {
		return rpc.RPCInstruction{ProgramIdIndex: idxSystem, Accounts: []int{nonceIdx, 9, 10}, Data: data(solSysInitializeNonce)}
	}
	transfer := rpc.RPCInstruction{ProgramIdIndex: idxSystem, Accounts: []int{idxVault, idxRecip}, Data: data(solSysTransfer)}
	fund := func(toIdx int) rpc.RPCInstruction {
		return rpc.RPCInstruction{ProgramIdIndex: idxSystem, Accounts: []int{idxVault, toIdx}, Data: data(solSysTransfer)}
	}
	allocate := func(idx int) rpc.RPCInstruction {
		return rpc.RPCInstruction{ProgramIdIndex: idxSystem, Accounts: []int{idx, idxVault}, Data: data(solSysAllocateWithSeed)}
	}
	memo := rpc.RPCInstruction{ProgramIdIndex: idxMemo, Accounts: []int{idxVault}, Data: base58.Encode([]byte("hi"))}

	cases := []struct {
		name string
		ins  []rpc.RPCInstruction
		want bool
	}{
		{"create-with-seed + init nonce (created==nonce) -> SKIP", []rpc.RPCInstruction{createWithSeed(idxNonce), initNonce(idxNonce)}, true},
		{"plain create-account + init nonce -> SKIP", []rpc.RPCInstruction{createAccount(idxNonce), initNonce(idxNonce)}, true},
		{"init-then-create order -> SKIP", []rpc.RPCInstruction{initNonce(idxNonce), createWithSeed(idxNonce)}, true},
		{"normal outbound (single transfer) -> keep", []rpc.RPCInstruction{transfer}, false},
		{"outbound with extra transfer -> keep", []rpc.RPCInstruction{transfer, transfer}, false},
		{"create+init+transfer combo -> keep", []rpc.RPCInstruction{createWithSeed(idxNonce), initNonce(idxNonce), transfer}, false},
		{"create+init + memo -> keep", []rpc.RPCInstruction{createWithSeed(idxNonce), initNonce(idxNonce), memo}, false},
		{"create+init but created != nonce -> keep", []rpc.RPCInstruction{createWithSeed(idxNonce), initNonce(idxRecip)}, false},
		{"create only (no init) -> keep", []rpc.RPCInstruction{createWithSeed(idxNonce)}, false},
		{"init only (no create) -> keep", []rpc.RPCInstruction{initNonce(idxNonce)}, false},
		{"empty -> keep", []rpc.RPCInstruction{}, false},
		{"adopt: allocate+fund+init -> SKIP", []rpc.RPCInstruction{allocate(idxNonce), fund(idxNonce), initNonce(idxNonce)}, true},
		{"adopt without funding -> keep", []rpc.RPCInstruction{allocate(idxNonce), initNonce(idxNonce)}, false},
		{"adopt funding another account -> keep", []rpc.RPCInstruction{allocate(idxNonce), fund(idxRecip), initNonce(idxNonce)}, false},
		{"adopt with two transfers -> keep", []rpc.RPCInstruction{allocate(idxNonce), fund(idxNonce), fund(idxNonce), initNonce(idxNonce)}, false},
		{"adopt allocating another account -> keep", []rpc.RPCInstruction{allocate(idxRecip), fund(idxNonce), initNonce(idxNonce)}, false},
		{"allocate + create -> keep", []rpc.RPCInstruction{allocate(idxNonce), createWithSeed(idxNonce), initNonce(idxNonce)}, false},
		{"two inits -> keep", []rpc.RPCInstruction{createWithSeed(idxNonce), initNonce(idxNonce), initNonce(idxNonce)}, false},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, isDurableNonceCreateTx(tc.ins, acctBy), tc.name)
	}
}
