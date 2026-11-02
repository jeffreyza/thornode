package pivxutil

import (
	"fmt"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	btctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"
)

// PayToPubKeyHashScript builds a P2PKH script for a 20-byte hash. The network
// version byte is not part of the script, so mainnet parameters are enough.
func PayToPubKeyHashScript(hash []byte) ([]byte, error) {
	address, err := btcutil.NewAddressPubKeyHash(hash, MainNetParams())
	if err != nil {
		return nil, err
	}
	return btctxscript.PayToAddrScript(address)
}

// SignatureHashes prepares every legacy SIGHASH_ALL digest before any
// signature script is attached. PIVX SIGVERSION_BASE serializes nVersion and
// nType as two int16s; version 1 type 0 matches Bitcoin's version int32, so
// CalcSignatureHash is the consensus digest for a transparent v1 transaction.
func SignatureHashes(tx *wire.MsgTx, previous []*wire.TxOut) ([][]byte, error) {
	if tx == nil {
		return nil, fmt.Errorf("missing PIVX transaction")
	}
	if tx.Version != 1 {
		return nil, fmt.Errorf("PIVX outbound must be legacy version 1, got %d", tx.Version)
	}
	if tx.HasWitness() {
		return nil, fmt.Errorf("PIVX outbound must not carry a witness")
	}
	if len(previous) != len(tx.TxIn) {
		return nil, fmt.Errorf("PIVX signing requires every previous output")
	}
	hashes := make([][]byte, len(previous))
	for i, output := range previous {
		if output == nil || output.Value <= 0 || len(output.PkScript) == 0 {
			return nil, fmt.Errorf("invalid PIVX previous output %d", i)
		}
		hash, err := btctxscript.CalcSignatureHash(output.PkScript, btctxscript.SigHashAll, tx, i)
		if err != nil {
			return nil, fmt.Errorf("fail to prepare PIVX input %d: %w", i, err)
		}
		hashes[i] = hash
	}
	return hashes, nil
}

// SignatureScript checks the signature against the digest, then builds a
// legacy scriptSig (signature || SIGHASH_ALL, compressed pubkey).
func SignatureScript(digest []byte, signature *btcec.Signature, pubKey *btcec.PublicKey) ([]byte, error) {
	if len(digest) != 32 {
		return nil, fmt.Errorf("PIVX signing digest: got %d bytes, want 32", len(digest))
	}
	if signature == nil || pubKey == nil {
		return nil, fmt.Errorf("missing PIVX signature or public key")
	}
	if !signature.Verify(digest, pubKey) {
		return nil, fmt.Errorf("fail to verify PIVX signature")
	}
	return btctxscript.NewScriptBuilder().
		AddData(append(signature.Serialize(), byte(btctxscript.SigHashAll))).
		AddData(pubKey.SerializeCompressed()).
		Script()
}

// VerifyTransaction executes each input after every signature script is set.
func VerifyTransaction(tx *wire.MsgTx, previous []*wire.TxOut) error {
	if tx == nil {
		return fmt.Errorf("missing PIVX transaction")
	}
	if tx.Version != 1 || tx.HasWitness() {
		return fmt.Errorf("PIVX verification requires a legacy version-1 transaction")
	}
	if len(previous) != len(tx.TxIn) {
		return fmt.Errorf("PIVX verification requires every previous output")
	}
	for i, output := range previous {
		if output == nil {
			return fmt.Errorf("missing PIVX previous output %d", i)
		}
		engine, err := btctxscript.NewEngine(output.PkScript, tx, i, btctxscript.StandardVerifyFlags, nil, nil, output.Value)
		if err != nil {
			return fmt.Errorf("fail to create PIVX script engine for input %d: %w", i, err)
		}
		if err = engine.Execute(); err != nil {
			return fmt.Errorf("fail to verify PIVX input %d: %w", i, err)
		}
	}
	return nil
}
