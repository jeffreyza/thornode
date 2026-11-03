package types

import (
	"crypto/ed25519"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
	. "gopkg.in/check.v1"
)

func TransactionTestPackage(t *testing.T) { TestingT(t) }

type TransactionTestSuite struct{}

var _ = Suite(&TransactionTestSuite{})

func (s *TransactionTestSuite) TestNewTransferTransaction(c *C) {
	from := MustPublicKeyFromString("9aE476sH92Vz7DMPyq5WLPkrKWivxeuTKEFKd2sZZcde")
	to := MustPublicKeyFromString("HEhDGuxaxGr9LuNtBdvbX2uggyAKoxYgHFaAiqxVu8UY")
	recentBlockHash := "9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo"
	memo := "test memo"
	amount := uint64(1000000)

	tx := NewTransferTransaction(from, to, amount, memo, recentBlockHash)

	c.Assert(len(tx.Signatures), Equals, 1)
	c.Assert(len(tx.Signatures[0]), Equals, 64)

	c.Assert(len(tx.Message.Accounts), Equals, 4)
	c.Assert(tx.Message.Accounts[0], DeepEquals, from)
	c.Assert(tx.Message.Accounts[1], DeepEquals, to)
	c.Assert(tx.Message.Accounts[2], DeepEquals, SystemProgramID)
	c.Assert(tx.Message.Accounts[3], DeepEquals, MemoProgramID)

	c.Assert(tx.Message.Header.NumRequireSignatures, Equals, uint8(1))
	c.Assert(tx.Message.Header.NumReadonlySignedAccounts, Equals, uint8(0))
	c.Assert(tx.Message.Header.NumReadonlyUnsignedAccounts, Equals, uint8(2))

	c.Assert(len(tx.Message.Instructions), Equals, 2)
	c.Assert(tx.Message.Instructions[0].ProgramIDIndex, Equals, 3)
	c.Assert(tx.Message.Instructions[0].Accounts, DeepEquals, []int{0})
	c.Assert(tx.Message.Instructions[0].Data, DeepEquals, []byte(memo))
	c.Assert(tx.Message.Instructions[1].ProgramIDIndex, Equals, 2)
	c.Assert(tx.Message.Instructions[1].Accounts, DeepEquals, []int{0, 1})

	c.Assert(tx.Message.RecentBlockHash, Equals, recentBlockHash)
}

func (s *TransactionTestSuite) TestNewTransaction(c *C) {
	header := MessageHeader{
		NumRequireSignatures:        2,
		NumReadonlySignedAccounts:   0,
		NumReadonlyUnsignedAccounts: 1,
	}
	msg := NewMessage(header, nil, "", nil)
	tx := NewTransaction(msg)

	c.Assert(len(tx.Signatures), Equals, 2)
	for _, sig := range tx.Signatures {
		c.Assert(len(sig), Equals, 64)
	}
}

func (s *TransactionTestSuite) TestTransactionSerialize(c *C) {
	from := MustPublicKeyFromString("9aE476sH92Vz7DMPyq5WLPkrKWivxeuTKEFKd2sZZcde")
	to := MustPublicKeyFromString("HEhDGuxaxGr9LuNtBdvbX2uggyAKoxYgHFaAiqxVu8UY")
	recentBlockHash := "9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo"

	tx := NewTransferTransaction(from, to, 100, "memo", recentBlockHash)
	data, err := tx.Serialize()
	c.Assert(err, IsNil)
	c.Assert(len(data) > 0, Equals, true)
}

func (s *TransactionTestSuite) TestTransactionSerializeSignatureMismatch(c *C) {
	header := MessageHeader{
		NumRequireSignatures:        1,
		NumReadonlySignedAccounts:   0,
		NumReadonlyUnsignedAccounts: 0,
	}
	msg := NewMessage(header, nil, "11111111111111111111111111111111", nil)
	tx := Transaction{
		Signatures: []Signature{make([]byte, 64), make([]byte, 64)},
		Message:    msg,
	}
	_, err := tx.Serialize()
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "invalid number of signatures")
}

func (s *TransactionTestSuite) TestTransactionSerializeInvalidBlockHash(c *C) {
	header := MessageHeader{
		NumRequireSignatures:        1,
		NumReadonlySignedAccounts:   0,
		NumReadonlyUnsignedAccounts: 0,
	}
	msg := NewMessage(header, nil, "not-valid-base58!!!", nil)
	tx := NewTransaction(msg)
	_, err := tx.Serialize()
	c.Assert(err, NotNil)
}

func (s *TransactionTestSuite) TestAddSignatureAndVerify(c *C) {
	pub, priv, err := ed25519.GenerateKey(nil)
	c.Assert(err, IsNil)

	var pubKey PublicKey
	copy(pubKey[:], pub)

	header := MessageHeader{
		NumRequireSignatures:        1,
		NumReadonlySignedAccounts:   0,
		NumReadonlyUnsignedAccounts: 0,
	}
	accounts := []PublicKey{pubKey}
	msg := NewMessage(header, accounts, "11111111111111111111111111111111", nil)
	tx := NewTransaction(msg)

	msgData, err := msg.Serialize()
	c.Assert(err, IsNil)
	sig := ed25519.Sign(priv, msgData)

	err = tx.AddSignature(sig)
	c.Assert(err, IsNil)
	c.Assert([]byte(tx.Signatures[0]), DeepEquals, sig)
}

func (s *TransactionTestSuite) TestAddSignatureNoMatch(c *C) {
	pub1, _, err := ed25519.GenerateKey(nil)
	c.Assert(err, IsNil)
	_, priv2, err := ed25519.GenerateKey(nil)
	c.Assert(err, IsNil)

	var pubKey1 PublicKey
	copy(pubKey1[:], pub1)

	header := MessageHeader{
		NumRequireSignatures:        1,
		NumReadonlySignedAccounts:   0,
		NumReadonlyUnsignedAccounts: 0,
	}
	accounts := []PublicKey{pubKey1}
	msg := NewMessage(header, accounts, "11111111111111111111111111111111", nil)
	tx := NewTransaction(msg)

	msgData, err := msg.Serialize()
	c.Assert(err, IsNil)
	sig := ed25519.Sign(priv2, msgData)

	err = tx.AddSignature(sig)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "no matching signer")
}

func (s *TransactionTestSuite) TestVerifySignatureSerializeError(c *C) {
	header := MessageHeader{
		NumRequireSignatures:        1,
		NumReadonlySignedAccounts:   0,
		NumReadonlyUnsignedAccounts: 0,
	}
	msg := Message{
		Header:          header,
		Accounts:        []PublicKey{{}},
		RecentBlockHash: "not-valid-base58!!!",
		Instructions:    nil,
	}
	tx := NewTransaction(msg)

	result := tx.verifySignature(0, make([]byte, 64))
	c.Assert(result, Equals, false)
}

func TestNewTransferTransactionOmitsEmptyMemoInstruction(t *testing.T) {
	from := MustPublicKeyFromString("E6wak7XKQtXVS8DqRRFnhToht7Grdz7BHWMQaCdJdaqc")
	to := MustPublicKeyFromString("2YpsG3TShyPDPWbtU5hTtGpb5XbKDQqaWVspVdWVaJSc")

	tx := NewTransferTransaction(from, to, 6_300_002, "", "ignored-for-test")

	require.Len(t, tx.Message.Accounts, 3)
	require.Equal(t, uint8(1), tx.Message.Header.NumReadonlyUnsignedAccounts)
	require.Len(t, tx.Message.Instructions, 1)
	require.Equal(t, 2, tx.Message.Instructions[0].ProgramIDIndex)
	require.Len(t, tx.Message.Instructions[0].Accounts, 2)
	require.Equal(t, []int{0, 1}, tx.Message.Instructions[0].Accounts)
	require.Equal(t, uint64(63_000_020), binary.LittleEndian.Uint64(tx.Message.Instructions[0].Data[4:12]))
}

func TestNewTransferTransactionIncludesMemoInstructionWhenPresent(t *testing.T) {
	from := MustPublicKeyFromString("E6wak7XKQtXVS8DqRRFnhToht7Grdz7BHWMQaCdJdaqc")
	to := MustPublicKeyFromString("2YpsG3TShyPDPWbtU5hTtGpb5XbKDQqaWVspVdWVaJSc")

	tx := NewTransferTransaction(from, to, 6_331_658, "r:00001", "ignored-for-test")

	require.Len(t, tx.Message.Accounts, 4)
	require.Equal(t, uint8(2), tx.Message.Header.NumReadonlyUnsignedAccounts)
	require.Len(t, tx.Message.Instructions, 2)
	require.Equal(t, 3, tx.Message.Instructions[0].ProgramIDIndex)
	require.Equal(t, []byte("r:00001"), tx.Message.Instructions[0].Data)
	require.Equal(t, 2, tx.Message.Instructions[1].ProgramIDIndex)
}

func TestNewSelfTransferTransactionListsVaultOnce(t *testing.T) {
	vault := MustPublicKeyFromString("E6wak7XKQtXVS8DqRRFnhToht7Grdz7BHWMQaCdJdaqc")

	tx := NewSelfTransferTransaction(vault, 200_123, "consolidate", "9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo")

	// vault, system program, memo program — the vault key appears exactly once,
	// or Solana rejects the transaction with AccountLoadedTwice
	require.Len(t, tx.Message.Accounts, 3)
	require.Equal(t, vault, tx.Message.Accounts[0])
	require.Equal(t, SystemProgramID, tx.Message.Accounts[1])
	require.Equal(t, MemoProgramID, tx.Message.Accounts[2])

	// only the vault signs and is writable; both programs are readonly
	require.Equal(t, uint8(1), tx.Message.Header.NumRequireSignatures)
	require.Equal(t, uint8(2), tx.Message.Header.NumReadonlyUnsignedAccounts)

	// memo first, then a transfer whose source and destination are both the vault
	require.Len(t, tx.Message.Instructions, 2)
	require.Equal(t, 2, tx.Message.Instructions[0].ProgramIDIndex)
	require.Equal(t, []byte("consolidate"), tx.Message.Instructions[0].Data)
	require.Equal(t, 1, tx.Message.Instructions[1].ProgramIDIndex)
	require.Equal(t, []int{0, 0}, tx.Message.Instructions[1].Accounts)
	require.Equal(t, uint64(2_001_230), binary.LittleEndian.Uint64(tx.Message.Instructions[1].Data[4:12]))

	// the message must serialize cleanly for TSS signing
	_, err := tx.Message.Serialize()
	require.NoError(t, err)
}

func TestNewSelfTransferTransactionWithoutMemo(t *testing.T) {
	vault := MustPublicKeyFromString("E6wak7XKQtXVS8DqRRFnhToht7Grdz7BHWMQaCdJdaqc")

	tx := NewSelfTransferTransaction(vault, 200_123, "", "ignored-for-test")

	require.Len(t, tx.Message.Accounts, 2)
	require.Equal(t, uint8(1), tx.Message.Header.NumReadonlyUnsignedAccounts)
	require.Len(t, tx.Message.Instructions, 1)
	require.Equal(t, 1, tx.Message.Instructions[0].ProgramIDIndex)
	require.Equal(t, []int{0, 0}, tx.Message.Instructions[0].Accounts)
}

// advanceNonceData is the SystemProgram AdvanceNonceAccount discriminator: enum
// index 4, 4-byte little-endian, no arguments.
var advanceNonceData = []byte{4, 0, 0, 0}

func TestNewNonceTransferTransactionWithMemo(t *testing.T) {
	from := MustPublicKeyFromString("E6wak7XKQtXVS8DqRRFnhToht7Grdz7BHWMQaCdJdaqc")
	to := MustPublicKeyFromString("2YpsG3TShyPDPWbtU5hTtGpb5XbKDQqaWVspVdWVaJSc")
	nonceAccount := MustPublicKeyFromString("9aE476sH92Vz7DMPyq5WLPkrKWivxeuTKEFKd2sZZcde")
	nonceAuthority := from
	// nonceValue occupies the message's recentBlockhash field, so it must be a
	// base58 blob the message serializer can decode.
	nonceValue := "9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo"

	tx := NewNonceTransferTransaction(from, to, 6_331_658, "r:00001", nonceValue, nonceAccount, nonceAuthority)

	// Account order: writable signer, writable non-signers, then readonly programs.
	require.Len(t, tx.Message.Accounts, 6)
	require.Equal(t, from, tx.Message.Accounts[0])
	require.Equal(t, to, tx.Message.Accounts[1])
	require.Equal(t, nonceAccount, tx.Message.Accounts[2])
	require.Equal(t, SystemProgramID, tx.Message.Accounts[3])
	require.Equal(t, SysVarRecentBlockhashesID, tx.Message.Accounts[4])
	require.Equal(t, MemoProgramID, tx.Message.Accounts[5])

	require.Equal(t, uint8(1), tx.Message.Header.NumRequireSignatures)
	require.Equal(t, uint8(0), tx.Message.Header.NumReadonlySignedAccounts)
	// readonly non-signers = SystemProgram + RecentBlockhashes sysvar + Memo
	require.Equal(t, uint8(3), tx.Message.Header.NumReadonlyUnsignedAccounts)

	// instruction[0] MUST be AdvanceNonceAccount, referencing System program (3), and
	// accounts [nonce(2), RecentBlockhashes sysvar(4), authority=from(0)] in that order.
	require.Len(t, tx.Message.Instructions, 3)
	require.Equal(t, 3, tx.Message.Instructions[0].ProgramIDIndex)
	require.Equal(t, []int{2, 4, 0}, tx.Message.Instructions[0].Accounts)
	require.Equal(t, advanceNonceData, tx.Message.Instructions[0].Data)

	// memo instruction, then the transfer (system program, from->to).
	require.Equal(t, 5, tx.Message.Instructions[1].ProgramIDIndex)
	require.Equal(t, []byte("r:00001"), tx.Message.Instructions[1].Data)
	require.Equal(t, 3, tx.Message.Instructions[2].ProgramIDIndex)
	require.Equal(t, []int{0, 1}, tx.Message.Instructions[2].Accounts)
	require.Equal(t, uint32(2), binary.LittleEndian.Uint32(tx.Message.Instructions[2].Data[0:4]))
	require.Equal(t, uint64(63_316_580), binary.LittleEndian.Uint64(tx.Message.Instructions[2].Data[4:12]))

	// the durable nonce is baked into the recentBlockhash field, unchanged.
	require.Equal(t, nonceValue, tx.Message.RecentBlockHash)

	// must serialize cleanly for TSS signing, and round-trip byte-identically so
	// every keysign party handed the same nonce produces the same msgID.
	data, err := tx.Message.Serialize()
	require.NoError(t, err)
	require.NotEmpty(t, data)
	again, err := tx.Message.Serialize()
	require.NoError(t, err)
	require.Equal(t, data, again)
}

func TestNewNonceTransferTransactionWithoutMemo(t *testing.T) {
	from := MustPublicKeyFromString("E6wak7XKQtXVS8DqRRFnhToht7Grdz7BHWMQaCdJdaqc")
	to := MustPublicKeyFromString("2YpsG3TShyPDPWbtU5hTtGpb5XbKDQqaWVspVdWVaJSc")
	nonceAccount := MustPublicKeyFromString("9aE476sH92Vz7DMPyq5WLPkrKWivxeuTKEFKd2sZZcde")
	nonceValue := "9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo"

	tx := NewNonceTransferTransaction(from, to, 200_123, "", nonceValue, nonceAccount, from)

	// no memo program appended
	require.Len(t, tx.Message.Accounts, 5)
	require.Equal(t, SystemProgramID, tx.Message.Accounts[3])
	require.Equal(t, SysVarRecentBlockhashesID, tx.Message.Accounts[4])
	require.Equal(t, uint8(2), tx.Message.Header.NumReadonlyUnsignedAccounts)

	// instruction[0] is still AdvanceNonce; transfer follows immediately (no memo).
	require.Len(t, tx.Message.Instructions, 2)
	require.Equal(t, 3, tx.Message.Instructions[0].ProgramIDIndex)
	require.Equal(t, []int{2, 4, 0}, tx.Message.Instructions[0].Accounts)
	require.Equal(t, advanceNonceData, tx.Message.Instructions[0].Data)
	require.Equal(t, 3, tx.Message.Instructions[1].ProgramIDIndex)
	require.Equal(t, []int{0, 1}, tx.Message.Instructions[1].Accounts)

	require.Equal(t, nonceValue, tx.Message.RecentBlockHash)
	_, err := tx.Message.Serialize()
	require.NoError(t, err)
}
