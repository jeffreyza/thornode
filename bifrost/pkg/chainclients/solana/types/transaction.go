package types

import (
	"crypto/ed25519"
	"fmt"
)

////////////////////////////////////////////////////////////////////////////////////////
// transaction.go contains the structs and functions to serialize and deserialize a solana transaction
////////////////////////////////////////////////////////////////////////////////////////

type Signature []byte

type Transaction struct {
	Signatures []Signature
	Message    Message
}

func NewTransferTransaction(from, to PublicKey, amount uint64, memo, recentBlockHash string) Transaction {
	accountKeys := []PublicKey{from, to, SystemProgramID}
	instructions := make([]CompiledInstruction, 0, 2)

	// 1e8 --> 1e9
	solValue := amount * 10

	if memo != "" {
		accountKeys = append(accountKeys, MemoProgramID)
		// Preserve the legacy memo-first ordering for memo-bearing transfers.
		instructions = append(instructions, NewCompiledInstruction(3, []int{0}, []byte(memo)))
	}

	// Transfer instruction
	// Program index is 2 (referencing accountKeys) for the system program
	// Account index is 0 for the vault/signer
	// Account index is 1 for the recipient
	instructions = append(instructions, NewCompiledTransferInstruction(2, 0, 1, solValue))

	header := MessageHeader{
		NumRequireSignatures:        1,
		NumReadonlySignedAccounts:   0,
		NumReadonlyUnsignedAccounts: uint8(len(accountKeys) - 2),
	}

	message := NewMessage(header, accountKeys, recentBlockHash, instructions)
	return NewTransaction(message)
}

// NewSelfTransferTransaction builds a transfer where the vault sends to its own
// address. The account list must contain the vault key exactly once — repeating
// it (as NewTransferTransaction would for from == to) makes the transaction
// invalid on Solana (AccountLoadedTwice) — so the transfer instruction
// references account index 0 for both source and destination. The runtime
// processes such a transfer with scoped borrows (debit then credit on the same
// account), netting to zero minus the transaction fee. Used by the vault
// keep-alive self-sign to land a fresh signature at a recent slot.
func NewSelfTransferTransaction(vault PublicKey, amount uint64, memo, recentBlockHash string) Transaction {
	accountKeys := []PublicKey{vault, SystemProgramID}
	instructions := make([]CompiledInstruction, 0, 2)

	// 1e8 --> 1e9
	solValue := amount * 10

	if memo != "" {
		accountKeys = append(accountKeys, MemoProgramID)
		// Preserve the legacy memo-first ordering for memo-bearing transfers.
		instructions = append(instructions, NewCompiledInstruction(2, []int{0}, []byte(memo)))
	}

	// Transfer instruction: program index 1 (system program), account index 0
	// (the vault) as both source and destination.
	instructions = append(instructions, NewCompiledTransferInstruction(1, 0, 0, solValue))

	header := MessageHeader{
		NumRequireSignatures:      1,
		NumReadonlySignedAccounts: 0,
		// Every account except the writable vault signer is a readonly program.
		NumReadonlyUnsignedAccounts: uint8(len(accountKeys) - 1),
	}

	message := NewMessage(header, accountKeys, recentBlockHash, instructions)
	return NewTransaction(message)
}

// NewNonceTransferTransaction builds a vault->recipient SOL transfer that uses a
// durable nonce instead of a recent blockhash, so the transaction does not expire
// during the multi-party TSS keysign+broadcast window. The message's
// recentBlockhash field carries the nonce account's STORED nonce (nonceValue), and
// an AdvanceNonceAccount instruction is prepended as instruction[0] — Solana
// requires the nonce advance to be the first instruction. The nonce authority is
// the vault (from), which already signs the transaction, so no extra signer is
// added.
//
// Determinism: the byte layout is a pure function of the inputs, so every keysign
// party handed the same nonceValue produces identical bytes. Reading a consistent
// nonceValue across all parties is the caller's responsibility.
//
// Account order follows Solana's rule (writable signers, readonly signers,
// writable non-signers, readonly non-signers):
//
//	0 from                       (writable signer; fee payer, source, nonce authority)
//	1 to                         (writable non-signer)
//	2 nonceAccount               (writable non-signer; AdvanceNonce mutates it)
//	3 SystemProgramID            (readonly non-signer; runs AdvanceNonce + Transfer)
//	4 RecentBlockhashes sysvar   (readonly non-signer)
//	5 MemoProgramID              (readonly non-signer; only when memo != "")
func NewNonceTransferTransaction(from, to PublicKey, amount uint64, memo, nonceValue string, nonceAccount, nonceAuthority PublicKey) Transaction {
	const (
		idxFrom      = 0
		idxTo        = 1
		idxNonce     = 2
		idxSystem    = 3
		idxSysvarRBH = 4
	)
	accountKeys := []PublicKey{from, to, nonceAccount, SystemProgramID, SysVarRecentBlockhashesID}

	idxMemoProgram := -1
	if memo != "" {
		idxMemoProgram = len(accountKeys) // 5
		accountKeys = append(accountKeys, MemoProgramID)
	}

	// The single signer is `from`, which is also the nonce authority in the vault
	// signing model, so AdvanceNonceAccount references the authority at idxFrom. A
	// distinct nonce authority would have to be added as an extra signer; that is
	// out of scope for the current model (vault-owned nonce accounts).
	_ = nonceAuthority

	// 1e8 --> 1e9
	solValue := amount * 10

	instructions := make([]CompiledInstruction, 0, 3)
	// instruction[0] MUST be AdvanceNonceAccount.
	instructions = append(instructions, NewCompiledAdvanceNonceInstruction(idxSystem, idxNonce, idxSysvarRBH, idxFrom))
	if memo != "" {
		instructions = append(instructions, NewCompiledInstruction(idxMemoProgram, []int{idxFrom}, []byte(memo)))
	}
	instructions = append(instructions, NewCompiledTransferInstruction(idxSystem, idxFrom, idxTo, solValue))

	header := MessageHeader{
		NumRequireSignatures:      1,
		NumReadonlySignedAccounts: 0,
		// Readonly non-signers = SystemProgram + RecentBlockhashes sysvar (+ Memo).
		// The two writable non-signers (to, nonceAccount) plus the writable signer
		// (from) are the remaining accounts.
		NumReadonlyUnsignedAccounts: uint8(len(accountKeys) - 3),
	}

	message := NewMessage(header, accountKeys, nonceValue, instructions)
	return NewTransaction(message)
}

// NonceAccountSize is the fixed byte length of a SystemProgram durable-nonce
// account (version + state + authority + stored hash + fee calculator). It is the
// `space` argument to CreateAccountWithSeed and, with the rent schedule, fixes the
// account's rent-exempt minimum.
const NonceAccountSize = 80

// NewCreateNonceAccountTransaction builds the bootstrap transaction that creates a
// vault's durable-nonce account at a seed-derived address and initializes it with
// the vault as nonce authority, in a single transaction. Unlike an outbound,
// this transaction uses a recent blockhash (a nonce cannot anchor its own creation),
// so its keysign is still subject to the ~150-slot expiry — it is issued at normal
// cadence with retry-until-landed, off the churn-storm path, and never blocks an
// outbound (which falls back to the recent-blockhash path while the nonce is absent).
//
// Determinism: every field (seed-derived nonce address, seed, lamports, space,
// owner, authority) is a pure function of the vault, so keysign parties handed the
// same consensus recentBlockHash produce identical bytes.
//
// Account order (writable signers, readonly signers, writable non-signers, readonly
// non-signers):
//
//	0 vault                      (writable signer; fee payer, funder, seed base, authority)
//	1 nonceAccount               (writable non-signer; created + initialized)
//	2 SystemProgramID            (readonly non-signer; runs both instructions)
//	3 RecentBlockhashes sysvar   (readonly non-signer; InitializeNonce input)
//	4 Rent sysvar                (readonly non-signer; InitializeNonce input)
func NewCreateNonceAccountTransaction(vault, nonceAccount PublicKey, seed string, lamports uint64, recentBlockHash string) Transaction {
	const (
		idxVault     = 0
		idxNonce     = 1
		idxSystem    = 2
		idxSysvarRBH = 3
		idxSysvarR   = 4
	)
	accountKeys := []PublicKey{vault, nonceAccount, SystemProgramID, SysVarRecentBlockhashesID, SysVarRentID}

	instructions := make([]CompiledInstruction, 0, 2)
	// CreateAccountWithSeed: base == funder == vault (idxVault referenced twice).
	instructions = append(instructions, NewCompiledCreateAccountWithSeedInstruction(
		idxSystem, idxVault, idxNonce, idxVault,
		vault, seed, lamports, NonceAccountSize, SystemProgramID,
	))
	// InitializeNonceAccount: authority = vault.
	instructions = append(instructions, NewCompiledInitializeNonceInstruction(
		idxSystem, idxNonce, idxSysvarRBH, idxSysvarR, vault,
	))

	header := MessageHeader{
		NumRequireSignatures:      1,
		NumReadonlySignedAccounts: 0,
		// Readonly non-signers = SystemProgram + RecentBlockhashes + Rent sysvars.
		NumReadonlyUnsignedAccounts: uint8(len(accountKeys) - 2),
	}

	message := NewMessage(header, accountKeys, recentBlockHash, instructions)
	return NewTransaction(message)
}

// NewAdoptNonceAccountTransaction provisions a vault's durable-nonce account at a
// seed-derived address that ALREADY holds lamports (anyone can pre-fund the public
// address, and CreateAccountWithSeed then fails with "account already in use"). A
// System-owned account with no data is allocated in place (AllocateWithSeed, signed by
// the vault as seed base), funded with the full rent, and initialized. The transfer is
// the full rent whatever the account already holds, so the bytes do not depend on the
// pre-funded amount and every keysign party builds the same tx. Account order matches
// NewCreateNonceAccountTransaction.
func NewAdoptNonceAccountTransaction(vault, nonceAccount PublicKey, seed string, lamports uint64, recentBlockHash string) Transaction {
	const (
		idxVault     = 0
		idxNonce     = 1
		idxSystem    = 2
		idxSysvarRBH = 3
		idxSysvarR   = 4
	)
	accountKeys := []PublicKey{vault, nonceAccount, SystemProgramID, SysVarRecentBlockhashesID, SysVarRentID}
	instructions := []CompiledInstruction{
		NewCompiledAllocateWithSeedInstruction(idxSystem, idxNonce, idxVault, vault, seed, NonceAccountSize, SystemProgramID),
		NewCompiledTransferInstruction(idxSystem, idxVault, idxNonce, lamports),
		NewCompiledInitializeNonceInstruction(idxSystem, idxNonce, idxSysvarRBH, idxSysvarR, vault),
	}
	header := MessageHeader{
		NumRequireSignatures:        1,
		NumReadonlySignedAccounts:   0,
		NumReadonlyUnsignedAccounts: uint8(len(accountKeys) - 2),
	}
	return NewTransaction(NewMessage(header, accountKeys, recentBlockHash, instructions))
}

// NewCloseNonceAccountTransaction builds a transaction that withdraws a vault's
// full durable-nonce balance to a recipient, reclaiming the rent and closing the
// account. Used at churn: the retiring vault sweeps its nonce rent to the
// incoming asgard (recipient != vault) or back to itself (recipient == vault). It
// uses a recent blockhash — WithdrawNonceAccount does not require the nonce to
// anchor it — and runs off the churn-storm path with retry-until-landed.
//
// The account list dedups when recipient == vault, so the vault (fee payer,
// authority, and possibly recipient) appears once. Account order follows Solana's
// rule; indices are resolved after dedup.
func NewCloseNonceAccountTransaction(vault, nonceAccount, recipient PublicKey, lamports uint64, recentBlockHash string) Transaction {
	sameRecipient := recipient == vault

	// Writable signer (vault) first, then writable non-signers (recipient when
	// distinct, nonce account), then readonly non-signers (System + two sysvars).
	accountKeys := []PublicKey{vault}
	idxVault := 0
	idxRecipient := idxVault
	if !sameRecipient {
		idxRecipient = len(accountKeys)
		accountKeys = append(accountKeys, recipient)
	}
	idxNonce := len(accountKeys)
	accountKeys = append(accountKeys, nonceAccount)
	idxSystem := len(accountKeys)
	accountKeys = append(accountKeys, SystemProgramID)
	idxSysvarRBH := len(accountKeys)
	accountKeys = append(accountKeys, SysVarRecentBlockhashesID)
	idxSysvarR := len(accountKeys)
	accountKeys = append(accountKeys, SysVarRentID)

	instructions := []CompiledInstruction{
		NewCompiledWithdrawNonceInstruction(
			idxSystem, idxNonce, idxRecipient, idxSysvarRBH, idxSysvarR, idxVault, lamports,
		),
	}

	header := MessageHeader{
		NumRequireSignatures:      1,
		NumReadonlySignedAccounts: 0,
		// Readonly non-signers = SystemProgram + RecentBlockhashes + Rent sysvars.
		NumReadonlyUnsignedAccounts: 3,
	}

	message := NewMessage(header, accountKeys, recentBlockHash, instructions)
	return NewTransaction(message)
}

func NewTransaction(message Message) Transaction {
	signatures := make([]Signature, 0, message.Header.NumRequireSignatures)
	for i := uint8(0); i < message.Header.NumRequireSignatures; i++ {
		signatures = append(signatures, make([]byte, 64))
	}
	return Transaction{
		Signatures: signatures,
		Message:    message,
	}
}

func (tx *Transaction) Serialize() ([]byte, error) {
	if len(tx.Signatures) != int(tx.Message.Header.NumRequireSignatures) {
		return nil, fmt.Errorf("invalid number of signatures")
	}

	signatureCount := UintToVarLenBytes(uint64(len(tx.Signatures)))
	messageData, err := tx.Message.Serialize()
	if err != nil {
		return nil, err
	}

	output := make([]byte, 0, len(signatureCount)+len(signatureCount)*64+len(messageData))
	output = append(output, signatureCount...)
	for _, sig := range tx.Signatures {
		output = append(output, sig...)
	}
	output = append(output, messageData...)

	return output, nil
}

func (tx *Transaction) AddSignature(sig []byte) error {
	for i := 0; i < int(tx.Message.Header.NumRequireSignatures); i++ {
		if tx.verifySignature(i, sig) {
			tx.Signatures[i] = sig
			return nil
		}
	}
	return fmt.Errorf("no matching signer")
}

func (tx *Transaction) verifySignature(acctIdx int, sig []byte) bool {
	data, err := tx.Message.Serialize()
	if err != nil {
		return false
	}
	a := tx.Message.Accounts[acctIdx]

	return ed25519.Verify(a.Bytes(), data, sig)
}
