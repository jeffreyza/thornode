package dashutil

import (
	"bytes"
	"encoding/hex"
	"sync"
	"testing"

	"github.com/dashpay/dashd-go/btcec/v2"
	"github.com/dashpay/dashd-go/btcec/v2/ecdsa"
	"github.com/dashpay/dashd-go/btcutil"
	"github.com/dashpay/dashd-go/chaincfg"
	"github.com/dashpay/dashd-go/chaincfg/chainhash"
	"github.com/dashpay/dashd-go/txscript"
	"github.com/dashpay/dashd-go/wire"
)

func TestDashCoreLegacySigningVector(t *testing.T) {
	// Dash Core src/test/data/tx_valid.json, CHECKSIGVERIFY followed by 1,
	// excluded flags NONE. The signature was supplied by Core, not this signer.
	raw, err := hex.DecodeString("01000000010001000000000000000000000000000000000000000000000000000000000000000000006a473044022067288ea50aa799543a536ff9306f8e1cba05b9c6b10951175b924f96732555ed022026d7b5265f38d21541519e4a1e55044d5b9e17e15cdbaf29ae3792e99e883e7a012103ba8c8b86dea131c22ab967e6dd99bdae8eff7a1f75a2c35f1f944109e3fe5e22ffffffff010000000000000000015100000000")
	if err != nil {
		t.Fatal(err)
	}
	tx := wire.NewMsgTx(1)
	err = tx.Deserialize(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	script, err := hex.DecodeString("76a9145b6462475454710f3c22f5fdf0b40704c92f25c388ad51")
	if err != nil {
		t.Fatal(err)
	}
	previous := []*wire.TxOut{wire.NewTxOut(1, script)} // legacy hashing does not commit to amounts
	err = VerifyTransaction(tx, previous)
	if err != nil {
		t.Fatal(err)
	}
	hashes, err := SignatureHashes(tx, previous)
	if err != nil {
		t.Fatal(err)
	}
	pushes, err := txscript.PushedData(tx.TxIn[0].SignatureScript)
	if err != nil {
		t.Fatal(err)
	}
	if len(pushes) != 2 {
		t.Fatalf("reference script pushes: got %d, want 2", len(pushes))
	}
	sig, err := ecdsa.ParseDERSignature(pushes[0][:len(pushes[0])-1])
	if err != nil {
		t.Fatal(err)
	}
	pubKey, err := btcec.ParsePubKey(pushes[1])
	if err != nil {
		t.Fatal(err)
	}
	if !sig.Verify(hashes[0], pubKey) {
		t.Fatal("Dash Core signature does not verify against prepared digest")
	}
}

func TestConcurrentDashSigningAndVerification(t *testing.T) {
	key, pubKey := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{1}, 32))
	address, err := btcutil.NewAddressPubKeyHash(btcutil.Hash160(pubKey.SerializeCompressed()), &chaincfg.MainNetParams)
	if err != nil {
		t.Fatal(err)
	}
	script, err := txscript.PayToAddrScript(address)
	if err != nil {
		t.Fatal(err)
	}
	tx := wire.NewMsgTx(2)
	previous := make([]*wire.TxOut, 24)
	for i := range previous {
		tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&chainhash.Hash{byte(i + 1)}, uint32(i)), nil, nil))
		previous[i] = wire.NewTxOut(100000, script)
	}
	tx.AddTxOut(wire.NewTxOut(2300000, script))
	hashes, err := SignatureHashes(tx, previous)
	if err != nil {
		t.Fatal(err)
	}
	// Digest preparation must agree with the established legacy signing API.
	want := make([][]byte, len(previous))
	for i := range want {
		want[i], err = txscript.RawTxInSignature(tx, i, script, txscript.SigHashAll, key)
		if err != nil {
			t.Fatal(err)
		}
	}
	scripts := make([][]byte, len(previous))
	var wg sync.WaitGroup
	for i := range previous {
		wg.Add(1)
		go func() {
			defer wg.Done()
			signature := ecdsa.Sign(key, hashes[i])
			var scriptErr error
			encoded := append(signature.Serialize(), byte(txscript.SigHashAll))
			if !bytes.Equal(encoded, want[i]) {
				t.Errorf("input %d changed its signature", i)
			}
			scripts[i], scriptErr = SignatureScript(hashes[i], signature, pubKey)
			if scriptErr != nil {
				t.Error(scriptErr)
			}
		}()
	}
	wg.Wait()
	for i, script := range scripts {
		tx.TxIn[i].SignatureScript = script
	}
	err = VerifyTransaction(tx, previous)
	if err != nil {
		t.Fatal(err)
	}
	// A changed payment invalidates otherwise well-formed, nonempty signatures.
	// In particular, NULLFAIL must be returned to the caller.
	tx.TxOut[0].Value++
	err = VerifyTransaction(tx, previous)
	if err == nil {
		t.Fatal("invalid signed transaction passed verification")
	}
	_, err = SignatureHashes(tx, previous[:1])
	if err == nil {
		t.Fatal("missing previous outputs accepted")
	}
	previous[1] = nil
	_, err = SignatureHashes(tx, previous)
	if err == nil {
		t.Fatal("nil previous output accepted")
	}
}

func TestDashTSSSignature(t *testing.T) {
	key, pubKey := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{1}, 32))
	digest := bytes.Repeat([]byte{2}, 32)
	compact, err := ecdsa.SignCompact(key, digest, true)
	if err != nil {
		t.Fatal(err)
	}
	raw := compact[1:] // TSS returns fixed-width R || S without the recovery byte.
	signature, err := TSSSignature(raw)
	if err != nil {
		t.Fatal(err)
	}
	script, err := SignatureScript(digest, signature, pubKey)
	if err != nil {
		t.Fatal(err)
	}
	pushes, err := txscript.PushedData(script)
	if err != nil {
		t.Fatal(err)
	}
	if len(pushes) != 2 {
		t.Fatalf("signature script pushes: got %d, want 2", len(pushes))
	}
	want := append(ecdsa.Sign(key, digest).Serialize(), byte(txscript.SigHashAll))
	if !bytes.Equal(pushes[0], want) {
		t.Fatalf("unexpected signature: got %x, want %x", pushes[0], want)
	}
	if !bytes.Equal(pushes[1], pubKey.SerializeCompressed()) {
		t.Fatalf("unexpected public key: %x", pushes[1])
	}
	// Both valid S encodings must produce the same low-S script.
	highS := append([]byte(nil), raw...)
	var scalar btcec.ModNScalar
	scalar.SetByteSlice(highS[32:])
	scalar.Negate().PutBytesUnchecked(highS[32:])
	signature, err = TSSSignature(highS)
	if err != nil {
		t.Fatal(err)
	}
	got, err := SignatureScript(digest, signature, pubKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, script) {
		t.Fatalf("S normalization changed script: got %x, want %x", got, script)
	}
	for name, invalid := range map[string][]byte{
		"missing":    nil,
		"short":      raw[:63],
		"long":       append(append([]byte(nil), raw...), 0),
		"zero R":     append(make([]byte, 32), raw[32:]...),
		"zero S":     append(append([]byte(nil), raw[:32]...), make([]byte, 32)...),
		"overflow R": append(bytes.Repeat([]byte{0xff}, 32), raw[32:]...),
		"overflow S": append(append([]byte(nil), raw[:32]...), bytes.Repeat([]byte{0xff}, 32)...),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := TSSSignature(invalid)
			if err == nil {
				t.Fatal("invalid TSS signature accepted")
			}
		})
	}
	_, wrongPubKey := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{3}, 32))
	for _, tc := range []struct {
		digest    []byte
		signature *ecdsa.Signature
		pubKey    *btcec.PublicKey
	}{
		{digest[:31], signature, pubKey},
		{bytes.Repeat([]byte{4}, 32), signature, pubKey},
		{digest, nil, pubKey},
		{digest, signature, nil},
		{digest, signature, wrongPubKey},
	} {
		_, err := SignatureScript(tc.digest, tc.signature, tc.pubKey)
		if err == nil {
			t.Fatal("invalid signature script accepted")
		}
	}
}
