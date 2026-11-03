package zcash

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

// TestV6Vectors checks native bytes, transaction IDs and every input digest against saved reference values.
func TestV6Vectors(t *testing.T) {
	raw, readErr := os.ReadFile("testdata/v6/vectors.json")
	if readErr != nil {
		t.Fatal(readErr)
	}
	var vectors []struct {
		Name, Bitcoin, Raw, TxID string
		Branch, Expiry           uint32
		Amounts                  []int64
		Scripts, Sighashes       []string
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	decode := func(s string) []byte {
		t.Helper()
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	hasDistinctScripts := false
	for _, vector := range vectors {
		// Check the fixture set even when -run selects only one subtest.
		for i := 1; i < len(vector.Scripts); i++ {
			if !bytes.Equal(decode(vector.Scripts[0]), decode(vector.Scripts[i])) {
				hasDistinctScripts = true
			}
		}
		t.Run(vector.Name, func(t *testing.T) {
			tx := &Tx{MsgTx: &wire.MsgTx{}, ExpiryHeight: vector.Expiry, ConsensusBranchID: vector.Branch}
			bitcoin := decode(vector.Bitcoin)
			if err := tx.MsgTx.DeserializeNoWitness(bytes.NewReader(bitcoin)); err != nil {
				t.Fatal(err)
			}
			inputCount := len(tx.TxIn)
			if inputCount == 0 ||
				len(vector.Amounts) != inputCount ||
				len(vector.Scripts) != inputCount ||
				len(vector.Sighashes) != inputCount {
				t.Fatal("fixture requires one amount, script and sighash per input")
			}
			previous := make([]*wire.TxOut, len(vector.Amounts))
			for i, value := range vector.Amounts {
				previous[i] = wire.NewTxOut(value, decode(vector.Scripts[i]))
			}
			var buffer bytes.Buffer
			err := tx.Serialize(&buffer)
			encoded := hex.EncodeToString(buffer.Bytes())
			if err != nil || encoded != vector.Raw {
				t.Fatal("native serialization mismatch", err)
			}
			restored, err := ParseTx(decode(encoded))
			if err != nil {
				t.Fatal(err)
			}
			id, err := restored.ID()
			if err != nil || id.String() != vector.TxID {
				t.Fatal("native txid mismatch", id, err)
			}
			if id == chainhash.DoubleHashH(decode(encoded)) {
				t.Fatal("v6 must not use SHA256d txid")
			}
			hashes, err := NewSigHashes(tx, previous)
			if err != nil {
				t.Fatal(err)
			}
			for i, want := range vector.Sighashes {
				digest, hashErr := hashes.SignatureHash(i)
				if hashErr != nil || hex.EncodeToString(digest) != want {
					t.Fatal("native signing digest mismatch", i, hashErr)
				}
			}
			tx.TxIn[0].SignatureScript = []byte{1, 2, 3}
			changed, err := tx.ID()
			if err != nil || changed != id {
				t.Fatal("authorizing script changed txid", err)
			}
			for _, field := range []string{"amount", "script"} {
				last := len(previous) - 1
				if field == "amount" {
					previous[last].Value++
				} else {
					previous[last].PkScript = []byte{0x51}
				}
				changedHashes, hashErr := NewSigHashes(tx, previous)
				if hashErr != nil {
					t.Fatal(hashErr)
				}
				digest, hashErr := changedHashes.SignatureHash(0)
				if hashErr != nil || hex.EncodeToString(digest) == vector.Sighashes[0] {
					t.Fatal("all-input commitment missing", field, hashErr)
				}
				previous[last] = wire.NewTxOut(vector.Amounts[last], decode(vector.Scripts[last]))
			}
			// Read-only caches remain valid as concurrent signers attach scripts.
			t.Run("concurrent signatures", func(t *testing.T) {
				for i := range tx.TxIn {
					t.Run(strconv.Itoa(i), func(t *testing.T) {
						t.Parallel()
						digest, hashErr := hashes.SignatureHash(i)
						if hashErr != nil {
							t.Fatal(hashErr)
						}
						tx.TxIn[i].SignatureScript = digest
					})
				}
			})
		})
	}
	if !hasDistinctScripts {
		t.Fatal("reference vectors must include inputs with distinct previous-output scripts")
	}
}

// TestParseTxLegacy preserves already-signed payloads and their pre-v5 transaction IDs.
func TestParseTxLegacy(t *testing.T) {
	v3, decodeErr := hex.DecodeString(legacyTx)
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	// Sapling adds a zero value balance and two empty Sapling counts before JoinSplit.
	v4 := append(bytes.Clone(v3[:len(v3)-1]), make([]byte, 11)...)
	binary.LittleEndian.PutUint32(v4[:4], 0x80000004)
	binary.LittleEndian.PutUint32(v4[4:8], 0x892f2085)
	for _, raw := range [][]byte{v3, v4} {
		tx, err := ParseTx(raw)
		if err != nil {
			t.Fatal(err)
		}
		if tx.ExpiryHeight != 215039 || len(tx.TxIn) != 1 || len(tx.TxOut) != 2 {
			t.Fatal("legacy fields changed", tx)
		}
		var encoded bytes.Buffer
		if err = tx.Serialize(&encoded); err != nil {
			t.Fatal(err)
		}
		id, err := tx.ID()
		if err != nil || id != chainhash.DoubleHashH(raw) || !bytes.Equal(raw, encoded.Bytes()) {
			t.Fatal("legacy payload or txid changed", err)
		}
		if _, err = NewSigHashes(tx, []*wire.TxOut{wire.NewTxOut(1, []byte{0x51})}); err == nil {
			t.Fatal("legacy payload accepted for new signing")
		}
		for i := range raw {
			if _, err = ParseTx(raw[:i]); err == nil {
				t.Fatal("accepted truncated legacy payload", i)
			}
		}
		if _, err = ParseTx(append(bytes.Clone(raw), 0)); err == nil {
			t.Fatal("accepted trailing legacy bytes")
		}
		raw[len(raw)-1] = 1
		if _, err = ParseTx(raw); err == nil {
			t.Fatal("accepted legacy shielded data")
		}
	}
}

// TestParseTxCanonicalBody checks that btcd rejects malformed transparent body encodings.
func TestParseTxCanonicalBody(t *testing.T) {
	tx := &Tx{MsgTx: wire.NewMsgTx(6), ConsensusBranchID: 0x37a5165b, ExpiryHeight: 120}
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{}, nil, nil))
	tx.AddTxOut(wire.NewTxOut(1, []byte{0x51}))
	var encoded bytes.Buffer
	if err := tx.Serialize(&encoded); err != nil {
		t.Fatal(err)
	}
	raw := encoded.Bytes()
	// Replace the single-input count with a non-canonical or impossible CompactSize.
	invalidCounts := [][]byte{
		{0xfd, 1, 0},
		{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	}
	for _, count := range invalidCounts {
		bad := append(bytes.Clone(raw[:20]), count...)
		bad = append(bad, raw[21:]...)
		if _, err := ParseTx(bad); err == nil {
			t.Fatal("accepted invalid input count", count)
		}
	}
	tx.TxOut[0].Value = -1
	if err := tx.Serialize(io.Discard); err == nil {
		t.Fatal("accepted negative output")
	}
	tx.TxOut[0].Value = 1
	tx.TxIn[0].Witness = wire.TxWitness{[]byte{1}}
	if err := tx.Serialize(io.Discard); err == nil {
		t.Fatal("accepted Bitcoin witness")
	}
	tx.TxIn[0] = nil
	if err := tx.Serialize(io.Discard); err == nil {
		t.Fatal("accepted nil input")
	}
}

// TestV6RejectsInvalidData rejects shielded bundles, missing branches and malformed payments.
func TestV6RejectsInvalidData(t *testing.T) {
	tx := &Tx{MsgTx: wire.NewMsgTx(6), ConsensusBranchID: 0x37a5165b, ExpiryHeight: 120}
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{}, nil, nil))
	tx.AddTxOut(wire.NewTxOut(1, []byte{0x51}))
	var encoded bytes.Buffer
	if err := tx.Serialize(&encoded); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		raw := bytes.Clone(encoded.Bytes())
		raw[len(raw)-i] = 1
		if _, err := ParseTx(raw); err == nil {
			t.Fatal("accepted shielded bundle", i)
		}
	}
	if _, err := ParseTx(append(bytes.Clone(encoded.Bytes()), 0)); err == nil {
		t.Fatal("accepted trailing bytes")
	}
	wrongVersion := bytes.Clone(encoded.Bytes())
	wrongVersion[0] = 4
	if _, err := ParseTx(wrongVersion); err == nil {
		t.Fatal("accepted mismatched version and group ID")
	}
	for i := 0; i < encoded.Len(); i++ {
		if _, err := ParseTx(encoded.Bytes()[:i]); err == nil {
			t.Fatal("accepted truncation", i)
		}
	}
	if _, err := NewSigHashes(tx, nil); err == nil {
		t.Fatal("accepted missing previous outputs")
	}
	for _, test := range []struct {
		name   string
		output *wire.TxOut
	}{
		{"nil previous output", nil},
		{"negative previous value", wire.NewTxOut(-1, []byte{0x51})},
		{"empty previous script", wire.NewTxOut(1, nil)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewSigHashes(tx, []*wire.TxOut{test.output}); err == nil {
				t.Fatal("accepted invalid previous output")
			}
		})
	}
	hashes, hashErr := NewSigHashes(tx, []*wire.TxOut{wire.NewTxOut(1, []byte{0x51})})
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	for _, index := range []int{-1, len(tx.TxIn)} {
		if _, err := hashes.SignatureHash(index); err == nil {
			t.Fatal("accepted invalid signing index", index)
		}
	}
	wrongBranch := bytes.Clone(encoded.Bytes())
	binary.LittleEndian.PutUint32(wrongBranch[8:12], 0)
	if _, err := ParseTx(wrongBranch); err == nil {
		t.Fatal("accepted zero branch in payload")
	}
	tx.ExpiryHeight = 499999999
	if err := tx.Serialize(io.Discard); err != nil {
		t.Fatal("rejected maximum expiry", err)
	}
	if _, err := tx.ID(); err != nil {
		t.Fatal("rejected maximum expiry", err)
	}
	tx.ExpiryHeight = 500000000
	if err := tx.Serialize(io.Discard); err == nil {
		t.Fatal("accepted excessive expiry")
	}
	if _, err := tx.ID(); err == nil {
		t.Fatal("accepted excessive expiry")
	}
	tx.ExpiryHeight = 120
	tx.ConsensusBranchID = 0
	if _, err := tx.ID(); err == nil {
		t.Fatal("accepted zero branch")
	}
}

func TestV6BranchBinding(t *testing.T) {
	tx := &Tx{MsgTx: wire.NewMsgTx(6), ExpiryHeight: 120}
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{}, nil, nil))
	tx.AddTxOut(wire.NewTxOut(1, []byte{0x51}))
	previous := []*wire.TxOut{wire.NewTxOut(2, []byte{0x51})}
	var previousID chainhash.Hash
	var previousDigest []byte
	for _, branch := range []uint32{0xabcdef12, 0x01020304} {
		tx.ConsensusBranchID = branch
		var encoded bytes.Buffer
		if err := tx.Serialize(&encoded); err != nil {
			t.Fatal(err)
		}
		if binary.LittleEndian.Uint32(encoded.Bytes()[8:12]) != branch {
			t.Fatal("incorrect branch in header")
		}
		restored, err := ParseTx(encoded.Bytes())
		if err != nil || restored.ConsensusBranchID != branch {
			t.Fatal("branch roundtrip failed", err)
		}
		id, err := restored.ID()
		if err != nil {
			t.Fatal(err)
		}
		hashes, err := NewSigHashes(restored, previous)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := hashes.SignatureHash(0)
		if err != nil {
			t.Fatal(err)
		}
		if previousDigest != nil && (id == previousID || bytes.Equal(digest, previousDigest)) {
			t.Fatal("branch change must change txid and signing digest")
		}
		previousID, previousDigest = id, digest
	}
}

// legacyTx is the previously supported signed Overwinter fixture.
const legacyTx = "030000807082c403011c15616e8b9a75ad4079a17bb296bcba8bda2712453baf1bde447bfe46be46e4010000006b48304502210093f8edae9784fee695d5ac5f84b4217084345a53c31c9e1e8e2a183ebe15cace02206872d90d0af77a4a4c18b761cf511e4583597ee5503e0e82e491da0f1a4377ed012103362327ee808f5961d26ef1a431386d6190638d67c14aa0e78e2eba1b58870cc0ffffffff02400d0300000000001976a9143b535da0ba90dad71ea005cccfe3cca47d746b3a88ac70d2dd11000000001976a914aefaebf9c83deba2ec76e080e2cec850dec161b188ac00000000ff47030000"
