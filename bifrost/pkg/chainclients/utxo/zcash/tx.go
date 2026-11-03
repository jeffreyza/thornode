package zcash

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/dchest/blake2b"
)

const (
	// v5GroupID is the ZIP-225 version-group id for transaction version 5. v5 is valid both
	// before and after NU7 (only v4 and earlier are rejected at NU7). THORChain now signs fresh
	// transparent txs as v6, but still accepts and resumes in-flight v5 checkpoints.
	v5GroupID uint32 = 0x26A7270A
	// v6GroupID is the ZIP-229 version-group id for transaction version 6. v6 is the standard
	// transparent format on mainnet since NU6.3 and is what THORChain signs for fresh outbounds.
	v6GroupID uint32 = 0xd884b698
)

// Tx contains transparent inputs/outputs and Zcash's additional header fields.
type Tx struct {
	*wire.MsgTx
	ExpiryHeight      uint32
	ConsensusBranchID uint32
}

// shieldedCapable reports whether the version uses the ZIP-225/244 (v5) or ZIP-229 (v6)
// header + BLAKE2b digest tree, as opposed to the legacy v3/v4 double-SHA256 path.
func (tx *Tx) shieldedCapable() bool { return tx.Version == 5 || tx.Version == 6 }

// headerWordAndGroup returns the 4-byte header word (fOverwintered|version) and the version
// group id for a v5 or v6 transaction.
func (tx *Tx) headerWordAndGroup() (uint32, uint32, error) {
	switch tx.Version {
	case 5:
		return 0x80000005, v5GroupID, nil
	case 6:
		return 0x80000006, v6GroupID, nil
	}
	return 0, 0, fmt.Errorf("unsupported Zcash shielded-capable version %d", tx.Version)
}

// Serialize writes a transparent transaction using btcd for its inputs and outputs.
func (tx *Tx) Serialize(w io.Writer) error {
	if err := tx.validate(); err != nil {
		return err
	}
	var bitcoin bytes.Buffer
	if err := tx.MsgTx.SerializeNoWitness(&bitcoin); err != nil {
		return err
	}
	var encoded []byte
	if tx.shieldedCapable() {
		header, err := tx.txHeader()
		if err != nil {
			return err
		}
		encoded = header
		encoded = append(encoded, bitcoin.Bytes()[4:bitcoin.Len()-4]...)
		if tx.Version == 6 {
			encoded = append(encoded, 0, 0, 0, 0) // Empty Sapling, Orchard and Ironwood bundles.
		} else {
			// v5 (ZIP-225): empty nSpendsSapling, nOutputsSapling and nActionsOrchard.
			encoded = append(encoded, 0, 0, 0)
		}
	} else {
		// Retain serialization only for recovery of already-signed v3/v4 payloads.
		group, tail := uint32(0x03c48270), 1
		if tx.Version == 4 {
			group, tail = 0x892f2085, 11
		}
		encoded = binary.LittleEndian.AppendUint32(encoded, uint32(tx.Version)|1<<31)
		encoded = binary.LittleEndian.AppendUint32(encoded, group)
		encoded = append(encoded, bitcoin.Bytes()[4:]...) // Includes lock time.
		encoded = binary.LittleEndian.AppendUint32(encoded, tx.ExpiryHeight)
		encoded = append(encoded, make([]byte, tail)...)
	}
	n, err := w.Write(encoded)
	if err == nil && n != len(encoded) {
		err = io.ErrShortWrite
	}
	return err
}

// ParseTx decodes transparent v6 and v5 payments (plus saved v3/v4 payloads) using
// btcd's wire codec.
func ParseTx(payload []byte) (*Tx, error) {
	if len(payload) < 8 || len(payload) > wire.MaxMessagePayload {
		return nil, fmt.Errorf("invalid Zcash transaction size")
	}
	header := binary.LittleEndian.Uint32(payload[:4])
	group := binary.LittleEndian.Uint32(payload[4:8])
	if header&0x80000000 == 0 {
		return nil, fmt.Errorf("Zcash transaction is not overwintered")
	}
	version := int32(header & 0x7fffffff)
	tx := &Tx{MsgTx: wire.NewMsgTx(version)}
	var body []byte
	switch {
	case version == 5 && group == v5GroupID, version == 6 && group == v6GroupID:
		bundleTail := 3 // v5: empty Sapling spends/outputs + Orchard actions.
		if version == 6 {
			bundleTail = 4
		}
		if len(payload) < 20+bundleTail+2 || !bytes.Equal(payload[len(payload)-bundleTail:], make([]byte, bundleTail)) {
			return nil, fmt.Errorf("incomplete or shielded Zcash v%d transaction", version)
		}
		tx.ConsensusBranchID = binary.LittleEndian.Uint32(payload[8:12])
		tx.LockTime = binary.LittleEndian.Uint32(payload[12:16])
		tx.ExpiryHeight = binary.LittleEndian.Uint32(payload[16:20])
		body = bytes.Clone(payload[20 : len(payload)-bundleTail])
		body = binary.LittleEndian.AppendUint32(body, tx.LockTime)
		if _, err := tx.txHeader(); err != nil {
			return nil, err
		}
	case version == 3 && group == 0x03c48270, version == 4 && group == 0x892f2085:
		tail := 5 // Expiry height and empty JoinSplit count.
		if version == 4 {
			tail = 15 // Also zero value balance and empty Sapling counts.
		}
		expiryOffset := len(payload) - tail
		if len(payload) < 14+tail || !bytes.Equal(payload[expiryOffset+4:], make([]byte, tail-4)) {
			return nil, fmt.Errorf("incomplete or shielded legacy Zcash transaction")
		}
		tx.ExpiryHeight = binary.LittleEndian.Uint32(payload[expiryOffset:])
		body = payload[8:expiryOffset] // Already includes lock time.
	default:
		return nil, fmt.Errorf("unsupported Zcash version %d or group ID %08x", version, group)
	}
	// Only Zcash's header and empty bundles differ; btcd validates the entire body,
	// including canonical CompactSize lengths and input/output allocation bounds.
	bodyReader := bytes.NewReader(body)
	versionBytes := binary.LittleEndian.AppendUint32(nil, uint32(version))
	reader := io.MultiReader(bytes.NewReader(versionBytes), bodyReader)
	if err := tx.MsgTx.DeserializeNoWitness(reader); err != nil {
		return nil, err
	}
	if bodyReader.Len() != 0 {
		return nil, fmt.Errorf("unexpected Zcash transaction data")
	}
	if err := tx.validate(); err != nil {
		return nil, err
	}
	return tx, nil
}

// validate rejects unsupported payment shapes before encoding or hashing.
func (tx *Tx) validate() error {
	// ZIP 203 limits ordinary payments to nExpiryHeight <= 499,999,999.
	if tx == nil || tx.MsgTx == nil || tx.ExpiryHeight >= 5e8 {
		return fmt.Errorf("invalid transparent Zcash transaction")
	}
	if tx.Version != 3 && tx.Version != 4 && tx.Version != 5 && tx.Version != 6 {
		return fmt.Errorf("unsupported Zcash transaction version %d", tx.Version)
	}
	if len(tx.TxIn) == 0 || len(tx.TxOut) == 0 {
		return fmt.Errorf("transparent Zcash payment needs inputs and outputs")
	}
	for _, input := range tx.TxIn {
		if input == nil || len(input.Witness) != 0 || len(input.SignatureScript) > txscript.MaxScriptSize {
			return fmt.Errorf("invalid Zcash input")
		}
	}
	for _, output := range tx.TxOut {
		if output == nil || output.Value < 0 || len(output.PkScript) > txscript.MaxScriptSize {
			return fmt.Errorf("invalid Zcash output")
		}
	}
	return nil
}

// txHeader encodes the ZIP-225 (v5) / ZIP-229 (v6) header for a transparent payment.
func (msg *Tx) txHeader() ([]byte, error) {
	if msg.MsgTx == nil || !msg.shieldedCapable() || msg.HasWitness() || msg.ExpiryHeight >= 5e8 {
		return nil, fmt.Errorf("invalid transparent Zcash v5/v6 transaction")
	}
	if msg.ConsensusBranchID == 0 {
		return nil, fmt.Errorf("missing Zcash consensus branch")
	}
	word, group, err := msg.headerWordAndGroup()
	if err != nil {
		return nil, err
	}
	header := binary.LittleEndian.AppendUint32(nil, word)
	header = binary.LittleEndian.AppendUint32(header, group)
	header = binary.LittleEndian.AppendUint32(header, msg.ConsensusBranchID)
	header = binary.LittleEndian.AppendUint32(header, msg.LockTime)
	return binary.LittleEndian.AppendUint32(header, msg.ExpiryHeight), nil
}

// txDigests holds transaction effects shared by all input signatures and the txid.
type txDigestSet struct {
	branch                                         uint32
	header, previous, sequences, outputs, shielded []byte
}

// SigHashes freezes every spent amount/script before concurrent input signing.
type SigHashes struct {
	*txDigestSet
	amounts, scripts []byte
	inputs           [][]byte
}

// shieldedPersonalizations returns the empty-bundle digest personalizations for the version.
// The transparent digest personalizations are version-independent (ZIP-244) and shared.
func (msg *Tx) shieldedPersonalizations() []string {
	if msg.Version == 6 {
		return []string{"ZTxIdSaplingHash", "ZTxIdOrchardH_v6", "ZTxIdIronwd_H_v6"}
	}
	// v5 (ZIP-244): Sapling + Orchard only, no Ironwood/ZSA.
	return []string{"ZTxIdSaplingHash", "ZTxIdOrchardHash"}
}

// txDigests computes ZIP-244 (v5) / ZIP-229 (v6) transaction effects, excluding authorizing
// scripts. The transparent digests are identical across v5/v6; only the header (version group
// id) and the empty-shielded personalizations differ.
func (msg *Tx) txDigests() (*txDigestSet, error) {
	if err := msg.validate(); err != nil {
		return nil, err
	}
	header, err := msg.txHeader()
	if err != nil {
		return nil, err
	}
	var previous, sequences, outputs bytes.Buffer
	for _, input := range msg.TxIn {
		previous.Write(input.PreviousOutPoint.Hash[:])
		previous.Write(binary.LittleEndian.AppendUint32(nil, input.PreviousOutPoint.Index))
		sequences.Write(binary.LittleEndian.AppendUint32(nil, input.Sequence))
	}
	for _, output := range msg.TxOut {
		if err = wire.WriteTxOut(&outputs, 0, 0, output); err != nil {
			return nil, err
		}
	}
	d := &txDigestSet{branch: msg.ConsensusBranchID}
	for _, part := range []struct {
		person string
		data   []byte
		target *[]byte
	}{
		{"ZTxIdHeadersHash", header, &d.header},
		{"ZTxIdPrevoutHash", previous.Bytes(), &d.previous},
		{"ZTxIdSequencHash", sequences.Bytes(), &d.sequences},
		{"ZTxIdOutputsHash", outputs.Bytes(), &d.outputs},
	} {
		*part.target, err = zecHash(part.person, part.data)
		if err != nil {
			return nil, err
		}
	}
	for _, person := range msg.shieldedPersonalizations() {
		digest, hashErr := zecHash(person)
		if hashErr != nil {
			return nil, hashErr
		}
		d.shielded = append(d.shielded, digest...)
	}
	return d, nil
}

// NewSigHashes commits to every previous output in transaction input order.
func NewSigHashes(tx *Tx, previous []*wire.TxOut) (*SigHashes, error) {
	d, err := tx.txDigests()
	if err != nil {
		return nil, err
	}
	if len(previous) != len(tx.TxIn) {
		return nil, fmt.Errorf("Zcash requires all previous outputs")
	}
	h := &SigHashes{txDigestSet: d, inputs: make([][]byte, len(previous))}
	var amounts, scripts bytes.Buffer
	for i, output := range previous {
		if output == nil || output.Value < 0 || len(output.PkScript) == 0 {
			return nil, fmt.Errorf("invalid Zcash previous output %d", i)
		}
		value := binary.LittleEndian.AppendUint64(nil, uint64(output.Value))
		var script bytes.Buffer
		if err = wire.WriteVarBytes(&script, 0, output.PkScript); err != nil {
			return nil, err
		}
		amounts.Write(value)
		scripts.Write(script.Bytes())
		input := tx.TxIn[i]
		outpoint := bytes.Clone(input.PreviousOutPoint.Hash[:])
		outpoint = binary.LittleEndian.AppendUint32(outpoint, input.PreviousOutPoint.Index)
		sequence := binary.LittleEndian.AppendUint32(nil, input.Sequence)
		h.inputs[i], err = zecHash("Zcash___TxInHash", outpoint, value, script.Bytes(), sequence)
		if err != nil {
			return nil, err
		}
	}
	h.amounts, err = zecHash("ZTxTrAmountsHash", amounts.Bytes())
	if err != nil {
		return nil, err
	}
	h.scripts, err = zecHash("ZTxTrScriptsHash", scripts.Bytes())
	return h, err
}

// SignatureHash returns ZIP-244 (v5) / ZIP-229 (v6) SIGHASH_ALL digest for one transparent input.
func (h *SigHashes) SignatureHash(index int) ([]byte, error) {
	if index < 0 || index >= len(h.inputs) {
		return nil, fmt.Errorf("Zcash signing input index out of range")
	}
	transparent, err := zecHash("ZTxIdTranspaHash", []byte{1}, h.previous, h.amounts, h.scripts, h.sequences, h.outputs, h.inputs[index])
	if err != nil {
		return nil, err
	}
	return h.root(transparent)
}

// root combines the header, transparent effects and empty shielded components.
func (d *txDigestSet) root(transparent []byte) ([]byte, error) {
	person := binary.LittleEndian.AppendUint32([]byte("ZcashTxHash_"), d.branch)
	return zecHash(string(person), d.header, transparent, d.shielded)
}

// zecHash hashes a protocol component with BLAKE2b's required personalization.
func zecHash(person string, parts ...[]byte) ([]byte, error) {
	h, err := blake2b.New(&blake2b.Config{Size: 32, Person: []byte(person)})
	if err != nil {
		return nil, err
	}
	for _, part := range parts {
		_, _ = h.Write(part)
	}
	return h.Sum(nil), nil
}

// ID returns the native transaction ID, rejecting invalid transaction state.
func (msg *Tx) ID() (chainhash.Hash, error) {
	if err := msg.validate(); err != nil {
		return chainhash.Hash{}, err
	}
	if !msg.shieldedCapable() {
		var raw bytes.Buffer
		if err := msg.Serialize(&raw); err != nil {
			return chainhash.Hash{}, err
		}
		return chainhash.DoubleHashH(raw.Bytes()), nil
	}
	d, err := msg.txDigests()
	if err != nil {
		return chainhash.Hash{}, err
	}
	transparent, err := zecHash("ZTxIdTranspaHash", d.previous, d.sequences, d.outputs)
	if err != nil {
		return chainhash.Hash{}, err
	}
	digest, err := d.root(transparent)
	var hash chainhash.Hash
	copy(hash[:], digest)
	return hash, err
}
