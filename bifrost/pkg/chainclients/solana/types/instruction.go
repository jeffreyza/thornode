package types

import (
	"encoding/binary"
)

type CompiledInstruction struct {
	ProgramIDIndex int
	Accounts       []int
	Data           []byte
}

func NewCompiledInstruction(programIDIndex int, accounts []int, data []byte) CompiledInstruction {
	return CompiledInstruction{
		ProgramIDIndex: programIDIndex,
		Accounts:       accounts,
		Data:           data,
	}
}

// Create transfer instruction data
func serializeTransferInstructionData(amount uint64) []byte {
	var data []byte

	// Append transfer instruction
	transferInstruction := uint32(2)
	transferInstructionBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(transferInstructionBytes, transferInstruction)
	data = append(data, transferInstructionBytes...)

	// Append amount
	amountBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(amountBytes, amount)
	data = append(data, amountBytes...)

	return data
}

// NewCompiledTransferInstruction creates a CompiledIntstruction for a transfer instruction
func NewCompiledTransferInstruction(programIndex, fromIndex, toIndex int, amount uint64) CompiledInstruction {
	accounts := []int{fromIndex, toIndex}
	data := serializeTransferInstructionData(amount)
	return NewCompiledInstruction(programIndex, accounts, data)
}

// serializeAdvanceNonceInstructionData encodes the SystemProgram AdvanceNonceAccount
// instruction: the 4-byte little-endian SystemInstruction enum index 4, with no
// further arguments.
func serializeAdvanceNonceInstructionData() []byte {
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, 4)
	return data
}

// NewCompiledAdvanceNonceInstruction builds the SystemProgram AdvanceNonceAccount
// instruction. Account order is fixed by the System program:
//
//	0: nonce account            (writable)
//	1: RecentBlockhashes sysvar (readonly)
//	2: nonce authority          (signer)
//
// This must be the first instruction in a durable-nonce transaction.
func NewCompiledAdvanceNonceInstruction(programIndex, nonceIndex, recentBlockhashesIndex, authorityIndex int) CompiledInstruction {
	accounts := []int{nonceIndex, recentBlockhashesIndex, authorityIndex}
	return NewCompiledInstruction(programIndex, accounts, serializeAdvanceNonceInstructionData())
}

// serializeCreateAccountWithSeedInstructionData encodes the SystemProgram
// CreateAccountWithSeed instruction (SystemInstruction enum index 3, 4-byte LE):
//
//	base:     Pubkey (32 bytes)
//	seed:     String — bincode-serialized as an 8-byte LE length prefix then bytes
//	lamports: u64 LE
//	space:    u64 LE
//	owner:    Pubkey (32 bytes)
//
// bincode serializes a String's length as a full u64, NOT a compact-u16 — this is
// distinct from the message-level compact length prefixes and must not be confused
// with them.
func serializeCreateAccountWithSeedInstructionData(base PublicKey, seed string, lamports, space uint64, owner PublicKey) []byte {
	data := make([]byte, 0, 4+32+8+len(seed)+8+8+32)

	disc := make([]byte, 4)
	binary.LittleEndian.PutUint32(disc, 3)
	data = append(data, disc...)

	data = append(data, base[:]...)

	seedLen := make([]byte, 8)
	binary.LittleEndian.PutUint64(seedLen, uint64(len(seed)))
	data = append(data, seedLen...)
	data = append(data, []byte(seed)...)

	lam := make([]byte, 8)
	binary.LittleEndian.PutUint64(lam, lamports)
	data = append(data, lam...)

	sp := make([]byte, 8)
	binary.LittleEndian.PutUint64(sp, space)
	data = append(data, sp...)

	data = append(data, owner[:]...)
	return data
}

// NewCompiledCreateAccountWithSeedInstruction builds the SystemProgram
// CreateAccountWithSeed instruction. Account order is fixed by the System program:
//
//	0: funding account (writable signer; pays lamports)
//	1: created account (writable non-signer; the seed-derived address)
//	2: base account    (signer; the seed base)
//
// When base == funding (the vault funds and is the seed base, as durable-nonce
// provisioning does), the caller passes the same account index for baseIndex and
// fundingIndex; the runtime accepts the repeated reference.
func NewCompiledCreateAccountWithSeedInstruction(programIndex, fundingIndex, createdIndex, baseIndex int, base PublicKey, seed string, lamports, space uint64, owner PublicKey) CompiledInstruction {
	accounts := []int{fundingIndex, createdIndex, baseIndex}
	data := serializeCreateAccountWithSeedInstructionData(base, seed, lamports, space, owner)
	return NewCompiledInstruction(programIndex, accounts, data)
}

// serializeAllocateWithSeedInstructionData encodes the SystemProgram AllocateWithSeed
// instruction (enum index 9): base Pubkey (32) | seed (u64 LE length + bytes) |
// space (u64 LE) | owner Pubkey (32).
func serializeAllocateWithSeedInstructionData(base PublicKey, seed string, space uint64, owner PublicKey) []byte {
	data := make([]byte, 0, 4+32+8+len(seed)+8+32)
	data = binary.LittleEndian.AppendUint32(data, 9)
	data = append(data, base[:]...)
	data = binary.LittleEndian.AppendUint64(data, uint64(len(seed)))
	data = append(data, []byte(seed)...)
	data = binary.LittleEndian.AppendUint64(data, space)
	data = append(data, owner[:]...)
	return data
}

// NewCompiledAllocateWithSeedInstruction builds the SystemProgram AllocateWithSeed
// instruction. Account order is fixed by the System program:
//
//	0: allocated account (writable; the seed-derived address)
//	1: base account      (signer; the seed base)
func NewCompiledAllocateWithSeedInstruction(programIndex, allocatedIndex, baseIndex int, base PublicKey, seed string, space uint64, owner PublicKey) CompiledInstruction {
	return NewCompiledInstruction(programIndex, []int{allocatedIndex, baseIndex}, serializeAllocateWithSeedInstructionData(base, seed, space, owner))
}

// serializeInitializeNonceInstructionData encodes the SystemProgram
// InitializeNonceAccount instruction (enum index 6, 4-byte LE) whose single
// argument is the nonce authority Pubkey (32 bytes).
func serializeInitializeNonceInstructionData(authority PublicKey) []byte {
	data := make([]byte, 0, 4+32)
	disc := make([]byte, 4)
	binary.LittleEndian.PutUint32(disc, 6)
	data = append(data, disc...)
	data = append(data, authority[:]...)
	return data
}

// NewCompiledInitializeNonceInstruction builds the SystemProgram
// InitializeNonceAccount instruction. Account order is fixed by the System program:
//
//	0: nonce account            (writable)
//	1: RecentBlockhashes sysvar (readonly)
//	2: Rent sysvar              (readonly)
func NewCompiledInitializeNonceInstruction(programIndex, nonceIndex, recentBlockhashesIndex, rentIndex int, authority PublicKey) CompiledInstruction {
	accounts := []int{nonceIndex, recentBlockhashesIndex, rentIndex}
	return NewCompiledInstruction(programIndex, accounts, serializeInitializeNonceInstructionData(authority))
}

// serializeWithdrawNonceInstructionData encodes the SystemProgram
// WithdrawNonceAccount instruction (enum index 5, 4-byte LE) whose single argument
// is the lamports to withdraw (u64 LE).
func serializeWithdrawNonceInstructionData(lamports uint64) []byte {
	data := make([]byte, 0, 4+8)
	disc := make([]byte, 4)
	binary.LittleEndian.PutUint32(disc, 5)
	data = append(data, disc...)
	lam := make([]byte, 8)
	binary.LittleEndian.PutUint64(lam, lamports)
	data = append(data, lam...)
	return data
}

// NewCompiledWithdrawNonceInstruction builds the SystemProgram WithdrawNonceAccount
// instruction, used to reclaim rent when closing a vault's nonce account at churn.
// Account order is fixed by the System program:
//
//	0: nonce account            (writable)
//	1: recipient                (writable)
//	2: RecentBlockhashes sysvar (readonly)
//	3: Rent sysvar              (readonly)
//	4: nonce authority          (signer)
func NewCompiledWithdrawNonceInstruction(programIndex, nonceIndex, recipientIndex, recentBlockhashesIndex, rentIndex, authorityIndex int, lamports uint64) CompiledInstruction {
	accounts := []int{nonceIndex, recipientIndex, recentBlockhashesIndex, rentIndex, authorityIndex}
	return NewCompiledInstruction(programIndex, accounts, serializeWithdrawNonceInstructionData(lamports))
}

type Instruction struct {
	ProgramID PublicKey
	Accounts  []AccountMeta
	Data      []byte
}
