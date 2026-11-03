package types

import (
	"crypto/sha256"
	"encoding/binary"

	. "gopkg.in/check.v1"
)

// NonceTestSuite covers the durable-nonce durable-nonce SDK primitives: the seed-derived
// address, the three SystemProgram instruction encoders, and the create/close
// transaction builders. The instruction-data byte layouts are asserted field by
// field because every keysign party must serialize identical bytes; a silent layout
// change here fragments the TSS party.
type NonceTestSuite struct{}

var _ = Suite(&NonceTestSuite{})

func le64(v uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return b
}

func (s *NonceTestSuite) TestCreateWithSeed(c *C) {
	base := MustPublicKeyFromString("9aE476sH92Vz7DMPyq5WLPkrKWivxeuTKEFKd2sZZcde")
	seed := "thornode-nonce-v1"

	got, err := CreateWithSeed(base, seed, SystemProgramID)
	c.Assert(err, IsNil)

	// Independent re-derivation of the documented layout sha256(base || seed || owner).
	h := sha256.New()
	h.Write(base[:])
	h.Write([]byte(seed))
	h.Write(SystemProgramID[:])
	want := PublicKeyFromBytes(h.Sum(nil))
	c.Assert(got, DeepEquals, want)

	// Deterministic: same inputs -> same address (the whole point — no shared state).
	got2, err := CreateWithSeed(base, seed, SystemProgramID)
	c.Assert(err, IsNil)
	c.Assert(got2, DeepEquals, got)

	// A different seed yields a different address.
	other, err := CreateWithSeed(base, "thornode-nonce-v2", SystemProgramID)
	c.Assert(err, IsNil)
	c.Assert(other == got, Equals, false)
}

func (s *NonceTestSuite) TestCreateWithSeedRejectsBadInputs(c *C) {
	base := MustPublicKeyFromString("9aE476sH92Vz7DMPyq5WLPkrKWivxeuTKEFKd2sZZcde")

	// Seed longer than MaxSeedLength (32) is rejected.
	long := ""
	for i := 0; i < MaxSeedLength+1; i++ {
		long += "a"
	}
	_, err := CreateWithSeed(base, long, SystemProgramID)
	c.Assert(err, NotNil)

	// An owner ending in the PDA marker is illegal for create_with_seed.
	var pdaOwner PublicKey
	marker := []byte("ProgramDerivedAddress")
	copy(pdaOwner[PublicKeyLength-len(marker):], marker)
	_, err = CreateWithSeed(base, "ok", pdaOwner)
	c.Assert(err, NotNil)
}

func (s *NonceTestSuite) TestCreateAccountWithSeedInstructionData(c *C) {
	base := MustPublicKeyFromString("9aE476sH92Vz7DMPyq5WLPkrKWivxeuTKEFKd2sZZcde")
	seed := "thornode-nonce-v1"
	lamports := uint64(1_500_000)
	space := uint64(NonceAccountSize)

	inst := NewCompiledCreateAccountWithSeedInstruction(2, 0, 1, 0, base, seed, lamports, space, SystemProgramID)
	c.Assert(inst.ProgramIDIndex, Equals, 2)
	c.Assert(inst.Accounts, DeepEquals, []int{0, 1, 0})

	d := inst.Data
	// disc(3) | base(32) | seedlen u64 | seed | lamports u64 | space u64 | owner(32)
	c.Assert(len(d), Equals, 4+32+8+len(seed)+8+8+32)
	c.Assert(d[0:4], DeepEquals, []byte{3, 0, 0, 0})
	c.Assert(d[4:36], DeepEquals, base[:])
	c.Assert(d[36:44], DeepEquals, le64(uint64(len(seed))))
	c.Assert(string(d[44:44+len(seed)]), Equals, seed)
	off := 44 + len(seed)
	c.Assert(d[off:off+8], DeepEquals, le64(lamports))
	c.Assert(d[off+8:off+16], DeepEquals, le64(space))
	c.Assert(d[off+16:off+48], DeepEquals, SystemProgramID[:])
}

func (s *NonceTestSuite) TestInitializeNonceInstructionData(c *C) {
	authority := MustPublicKeyFromString("HEhDGuxaxGr9LuNtBdvbX2uggyAKoxYgHFaAiqxVu8UY")
	inst := NewCompiledInitializeNonceInstruction(2, 1, 3, 4, authority)
	c.Assert(inst.ProgramIDIndex, Equals, 2)
	c.Assert(inst.Accounts, DeepEquals, []int{1, 3, 4})
	c.Assert(len(inst.Data), Equals, 4+32)
	c.Assert(inst.Data[0:4], DeepEquals, []byte{6, 0, 0, 0})
	c.Assert(inst.Data[4:36], DeepEquals, authority[:])
}

func (s *NonceTestSuite) TestWithdrawNonceInstructionData(c *C) {
	lamports := uint64(1_447_680)
	inst := NewCompiledWithdrawNonceInstruction(2, 1, 0, 3, 4, 0, lamports)
	c.Assert(inst.ProgramIDIndex, Equals, 2)
	c.Assert(inst.Accounts, DeepEquals, []int{1, 0, 3, 4, 0})
	c.Assert(len(inst.Data), Equals, 4+8)
	c.Assert(inst.Data[0:4], DeepEquals, []byte{5, 0, 0, 0})
	c.Assert(inst.Data[4:12], DeepEquals, le64(lamports))
}

func (s *NonceTestSuite) TestNewCreateNonceAccountTransaction(c *C) {
	vault := MustPublicKeyFromString("9aE476sH92Vz7DMPyq5WLPkrKWivxeuTKEFKd2sZZcde")
	seed := "thornode-nonce-v1"
	nonceAcct, err := CreateWithSeed(vault, seed, SystemProgramID)
	c.Assert(err, IsNil)
	recentBlockHash := "9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo"

	tx := NewCreateNonceAccountTransaction(vault, nonceAcct, seed, 1_500_000, recentBlockHash)

	c.Assert(len(tx.Signatures), Equals, 1)
	c.Assert(tx.Message.Accounts, DeepEquals, []PublicKey{
		vault, nonceAcct, SystemProgramID, SysVarRecentBlockhashesID, SysVarRentID,
	})
	c.Assert(tx.Message.Header.NumRequireSignatures, Equals, uint8(1))
	c.Assert(tx.Message.Header.NumReadonlySignedAccounts, Equals, uint8(0))
	c.Assert(tx.Message.Header.NumReadonlyUnsignedAccounts, Equals, uint8(3))

	c.Assert(len(tx.Message.Instructions), Equals, 2)
	// [0] CreateAccountWithSeed: funder(0), created(1), base(0)
	c.Assert(tx.Message.Instructions[0].ProgramIDIndex, Equals, 2)
	c.Assert(tx.Message.Instructions[0].Accounts, DeepEquals, []int{0, 1, 0})
	c.Assert(tx.Message.Instructions[0].Data[0:4], DeepEquals, []byte{3, 0, 0, 0})
	// [1] InitializeNonceAccount: nonce(1), recentbh(3), rent(4)
	c.Assert(tx.Message.Instructions[1].ProgramIDIndex, Equals, 2)
	c.Assert(tx.Message.Instructions[1].Accounts, DeepEquals, []int{1, 3, 4})
	c.Assert(tx.Message.Instructions[1].Data[0:4], DeepEquals, []byte{6, 0, 0, 0})
	// authority baked into InitializeNonce is the vault.
	c.Assert(tx.Message.Instructions[1].Data[4:36], DeepEquals, vault[:])

	c.Assert(tx.Message.RecentBlockHash, Equals, recentBlockHash)
	raw, err := tx.Serialize()
	c.Assert(err, IsNil)
	c.Assert(len(raw) > 0, Equals, true)
}

func (s *NonceTestSuite) TestNewCloseNonceAccountTransactionDistinctRecipient(c *C) {
	vault := MustPublicKeyFromString("9aE476sH92Vz7DMPyq5WLPkrKWivxeuTKEFKd2sZZcde")
	recipient := MustPublicKeyFromString("HEhDGuxaxGr9LuNtBdvbX2uggyAKoxYgHFaAiqxVu8UY")
	nonceAcct, err := CreateWithSeed(vault, "thornode-nonce-v1", SystemProgramID)
	c.Assert(err, IsNil)
	recentBlockHash := "9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo"

	tx := NewCloseNonceAccountTransaction(vault, nonceAcct, recipient, 1_447_680, recentBlockHash)

	// vault(0, writable signer), recipient(1), nonce(2), System(3), RecentBH(4), Rent(5)
	c.Assert(tx.Message.Accounts, DeepEquals, []PublicKey{
		vault, recipient, nonceAcct, SystemProgramID, SysVarRecentBlockhashesID, SysVarRentID,
	})
	c.Assert(tx.Message.Header.NumRequireSignatures, Equals, uint8(1))
	c.Assert(tx.Message.Header.NumReadonlyUnsignedAccounts, Equals, uint8(3))
	c.Assert(len(tx.Message.Instructions), Equals, 1)
	c.Assert(tx.Message.Instructions[0].ProgramIDIndex, Equals, 3)
	// WithdrawNonce accounts: nonce(2), recipient(1), recentbh(4), rent(5), authority(0)
	c.Assert(tx.Message.Instructions[0].Accounts, DeepEquals, []int{2, 1, 4, 5, 0})
	c.Assert(tx.Message.Instructions[0].Data[0:4], DeepEquals, []byte{5, 0, 0, 0})
	c.Assert(tx.Message.Instructions[0].Data[4:12], DeepEquals, le64(1_447_680))
}

func (s *NonceTestSuite) TestNewCloseNonceAccountTransactionSelfRecipient(c *C) {
	vault := MustPublicKeyFromString("9aE476sH92Vz7DMPyq5WLPkrKWivxeuTKEFKd2sZZcde")
	nonceAcct, err := CreateWithSeed(vault, "thornode-nonce-v1", SystemProgramID)
	c.Assert(err, IsNil)

	tx := NewCloseNonceAccountTransaction(vault, nonceAcct, vault, 1_447_680, "9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo")

	// recipient == vault dedups: vault(0), nonce(1), System(2), RecentBH(3), Rent(4)
	c.Assert(tx.Message.Accounts, DeepEquals, []PublicKey{
		vault, nonceAcct, SystemProgramID, SysVarRecentBlockhashesID, SysVarRentID,
	})
	c.Assert(tx.Message.Instructions[0].ProgramIDIndex, Equals, 2)
	// nonce(1), recipient(0), recentbh(3), rent(4), authority(0)
	c.Assert(tx.Message.Instructions[0].Accounts, DeepEquals, []int{1, 0, 3, 4, 0})
}
