package pivxutil

import (
	"encoding/binary"
	"encoding/hex"

	"github.com/btcsuite/btcd/btcjson"
)

// Opcodes from PIVX src/script/script.h. Cold-staking and exchange outputs are
// not spendable by the transparent vault key and are skipped while scanning.
const (
	opCheckColdStakeVerifyLOF byte = 0xd1
	opCheckColdStakeVerify    byte = 0xd2
	opExchangeAddr            byte = 0xe0
)

// TxVersionAndType reads the leading int16 nVersion and int16 nType from a
// serialized PIVX transaction. Bitcoin's version int32 is those two fields
// concatenated, so a legacy transparent tx is version 1, type 0.
func TxVersionAndType(rawHex string) (version int16, txType int16, ok bool) {
	raw, err := hex.DecodeString(rawHex)
	if err != nil || len(raw) < 4 {
		return 0, 0, false
	}
	version = int16(binary.LittleEndian.Uint16(raw[:2]))
	txType = int16(binary.LittleEndian.Uint16(raw[2:4]))
	return version, txType, true
}

// IsRejectedOutputScript reports cold-staking (P2CS) and exchange scripts.
// Data pushes are skipped, so a pubkey hash that happens to contain 0xd1,
// 0xd2, or 0xe0 is not treated as one of those opcodes.
func IsRejectedOutputScript(scriptHex string) bool {
	script, err := hex.DecodeString(scriptHex)
	if err != nil || len(script) == 0 {
		return false
	}
	for i := 0; i < len(script); {
		op := script[i]
		i++
		switch op {
		case opCheckColdStakeVerify, opCheckColdStakeVerifyLOF, opExchangeAddr:
			return true
		}
		n, ok := pushDataLength(script, op, &i)
		if !ok || i+n > len(script) {
			return false
		}
		i += n
	}
	return false
}

// pushDataLength reports how many bytes a push opcode consumes. i is advanced
// past the push-length prefix. Non-push opcodes consume no data.
func pushDataLength(script []byte, op byte, i *int) (int, bool) {
	switch {
	case op >= 0x01 && op <= 0x4b:
		return int(op), true
	case op == 0x4c:
		if *i >= len(script) {
			return 0, false
		}
		n := int(script[*i])
		*i++
		return n, true
	case op == 0x4d:
		if *i+2 > len(script) {
			return 0, false
		}
		n := int(binary.LittleEndian.Uint16(script[*i:]))
		*i += 2
		return n, true
	case op == 0x4e:
		if *i+4 > len(script) {
			return 0, false
		}
		n := int(binary.LittleEndian.Uint32(script[*i:]))
		*i += 4
		if n < 0 {
			return 0, false
		}
		return n, true
	default:
		return 0, true
	}
}

// IsCoinStake reports a proof-of-stake coinstake. PIVX defines it as a
// non-coinbase transaction whose first output is empty (value 0, empty script)
// and which has at least one more output for the stake plus block reward.
func IsCoinStake(tx *btcjson.TxRawResult) bool {
	if tx == nil || len(tx.Vin) == 0 || len(tx.Vout) < 2 {
		return false
	}
	if tx.Vin[0].IsCoinBase() {
		return false
	}
	return emptyOutput(tx.Vout[0])
}

func emptyOutput(out btcjson.Vout) bool {
	if out.Value != 0 {
		return false
	}
	script := out.ScriptPubKey.Hex
	return script == "" || script == "00"
}

// SkipTx reports transactions the scanner must not treat as deposits:
// special transactions (provider registration, LLMQ commitments, and the
// other non-zero types), Sapling transactions with no transparent input, and
// coinstake transactions whose outputs mix the staked principal with the
// block reward and masternode payout.
func SkipTx(tx *btcjson.TxRawResult) bool {
	if tx == nil {
		return true
	}
	if IsCoinStake(tx) {
		return true
	}
	version, txType, ok := TxVersionAndType(tx.Hex)
	if !ok {
		// Verbose responses from pivxd include hex. Without it, only the
		// coinstake shape above can be classified.
		return false
	}
	if txType != 0 {
		return true
	}
	// Sapling is version 3. A shielded deposit with no transparent input has
	// no sender the shared client can attribute. Mixed transactions that spend
	// a transparent input are left for the normal scanner.
	if version >= 3 && len(tx.Vin) == 0 {
		return true
	}
	if version != 1 && version != 3 {
		return true
	}
	return false
}
