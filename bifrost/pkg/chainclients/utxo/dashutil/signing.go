package dashutil

import (
	"fmt"

	"github.com/dashpay/dashd-go/btcec/v2"
	"github.com/dashpay/dashd-go/btcec/v2/ecdsa"
	"github.com/dashpay/dashd-go/txscript"
	"github.com/dashpay/dashd-go/wire"
)

// TSSSignature converts TSS's fixed-width R || S result to the upstream type.
func TSSSignature(raw []byte) (*ecdsa.Signature, error) {
	if len(raw) != 64 {
		return nil, fmt.Errorf("Dash TSS signature: got %d bytes, want 64", len(raw))
	}
	var r, s btcec.ModNScalar
	if r.SetByteSlice(raw[:32]) || s.SetByteSlice(raw[32:]) || r.IsZero() || s.IsZero() {
		return nil, fmt.Errorf("invalid Dash TSS signature scalar")
	}
	return ecdsa.NewSignature(&r, &s), nil
}

// SignatureScript verifies the external signature before serializing it. The
// upstream serializer enforces low-S encoding; script construction stays upstream.
func SignatureScript(digest []byte, signature *ecdsa.Signature, pubKey *btcec.PublicKey) ([]byte, error) {
	if len(digest) != 32 {
		return nil, fmt.Errorf("Dash signing digest: got %d bytes, want 32", len(digest))
	}
	if signature == nil || pubKey == nil {
		return nil, fmt.Errorf("missing Dash signature or public key")
	}
	if !signature.Verify(digest, pubKey) {
		return nil, fmt.Errorf("fail to verify Dash signature")
	}
	return txscript.NewScriptBuilder().
		AddData(append(signature.Serialize(), byte(txscript.SigHashAll))).
		AddData(pubKey.SerializeCompressed()).Script()
}

// SignatureHashes prepares all legacy SIGHASH_ALL digests before any signatures
// are attached. The returned digests do not retain references to the transaction.
func SignatureHashes(tx *wire.MsgTx, previous []*wire.TxOut) ([][]byte, error) {
	if len(previous) != len(tx.TxIn) {
		return nil, fmt.Errorf("Dash signing requires every previous output")
	}
	hashes := make([][]byte, len(previous))
	for i, output := range previous {
		if output == nil || output.Value <= 0 || len(output.PkScript) == 0 {
			return nil, fmt.Errorf("invalid Dash previous output %d", i)
		}
		hash, err := txscript.CalcSignatureHash(output.PkScript, txscript.SigHashAll, tx, i)
		if err != nil {
			return nil, fmt.Errorf("fail to prepare Dash input %d: %w", i, err)
		}
		hashes[i] = hash
	}
	return hashes, nil
}

// VerifyTransaction runs only after every worker has finished and every input's
// signature script has been attached. All script failures prevent broadcast.
func VerifyTransaction(tx *wire.MsgTx, previous []*wire.TxOut) error {
	if len(previous) != len(tx.TxIn) {
		return fmt.Errorf("Dash verification requires every previous output")
	}
	for i, output := range previous {
		if output == nil {
			return fmt.Errorf("missing Dash previous output %d", i)
		}
		engine, err := txscript.NewEngine(output.PkScript, tx, i, txscript.StandardVerifyFlags, nil, nil, output.Value)
		if err != nil {
			return fmt.Errorf("fail to create Dash script engine for input %d: %w", i, err)
		}
		err = engine.Execute()
		if err != nil {
			return fmt.Errorf("fail to verify Dash input %d: %w", i, err)
		}
	}
	return nil
}
