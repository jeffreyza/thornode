package pivxutil

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/btcsuite/btcutil/base58"
	btctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"
)

func TestDecodeAddressRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte{0x11}, 20)
	for _, params := range []*chaincfg.Params{MainNetParams(), RegressionNetParams()} {
		p2pkh, err := btcutil.NewAddressPubKeyHash(payload, params)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeAddress(p2pkh.String(), params)
		if err != nil {
			t.Fatalf("%s: %v", p2pkh, err)
		}
		if got.String() != p2pkh.String() {
			t.Fatalf("round trip %s -> %s", p2pkh, got)
		}
		p2sh, err := btcutil.NewAddressScriptHashFromHash(payload, params)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = DecodeAddress(p2sh.String(), params); err != nil {
			t.Fatalf("p2sh %s: %v", p2sh, err)
		}
	}
	main, err := btcutil.NewAddressPubKeyHash(payload, MainNetParams())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeAddress(main.String(), RegressionNetParams()); err == nil {
		t.Fatal("accepted a mainnet address on regtest")
	}
	if main.String()[0] != 'D' {
		t.Fatalf("mainnet P2PKH %s does not start with D", main)
	}
}

func TestRejectStakingAndExchange(t *testing.T) {
	hash := bytes.Repeat([]byte{0x22}, 20)
	staking := base58Check(append([]byte{MainNetStakingID}, hash...))
	if staking[0] != 'S' {
		t.Fatalf("staking address %s does not start with S", staking)
	}
	if _, err := DecodeAddress(staking, MainNetParams()); err == nil {
		t.Fatal("accepted mainnet staking address")
	}
	regStake := base58Check(append([]byte{RegTestStakingID}, hash...))
	if regStake[0] != 'W' {
		t.Fatalf("regtest staking address %s does not start with W", regStake)
	}
	if _, err := DecodeAddress(regStake, RegressionNetParams()); err == nil {
		t.Fatal("accepted regtest staking address")
	}
	exchange := base58Check(append([]byte{0x01, 0xb9, 0xa2}, hash...))
	if len(exchange) < 3 || exchange[:3] != "EXM" {
		t.Fatalf("exchange address %s does not start with EXM", exchange)
	}
	if _, err := DecodeAddress(exchange, MainNetParams()); err == nil {
		t.Fatal("accepted EXM exchange address")
	}
	ext := base58Check(append([]byte{0x01, 0xb9, 0xb1}, hash...))
	if len(ext) < 3 || ext[:3] != "EXT" {
		t.Fatalf("exchange address %s does not start with EXT", ext)
	}
	if _, err := DecodeAddress(ext, RegressionNetParams()); err == nil {
		t.Fatal("accepted EXT exchange address")
	}
	if _, err := DecodeAddress("0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798", MainNetParams()); err == nil {
		t.Fatal("accepted bare pubkey")
	}
}

func TestFeeRateFromRegtestFixture(t *testing.T) {
	// pivxd 5.6.1 getblockindexstats after a 225-byte tx with a 0.0001 PIV fee.
	rate, ok := FeeRateSatsPerByte("0.00044444", 225)
	if !ok || rate != 45 {
		t.Fatalf("got %d ok=%v", rate, ok)
	}
	if _, ok = FeeRateSatsPerByte("0.00", 0); ok {
		t.Fatal("empty block should not be a sample")
	}
	if _, ok = FeeRateSatsPerByte("-1", 100); ok {
		t.Fatal("negative estimatesmartfee-style value")
	}
}

func TestSubsidySchedule(t *testing.T) {
	const coin int64 = 100_000_000
	if got := BlockSubsidySats(true, 1); got != 250*coin {
		t.Fatalf("regtest height 1: %d", got)
	}
	if got := BlockSubsidySats(true, 576); got != 250*coin {
		t.Fatalf("regtest height 576: %d", got)
	}
	if got := BlockSubsidySats(true, 577); got != 10*coin {
		t.Fatalf("regtest height 577: %d", got)
	}
	if got := BlockSubsidySats(false, 1); got != 60001*coin {
		t.Fatalf("mainnet premine: %d", got)
	}
	if got := BlockSubsidySats(false, 3_715_201); got != 10*coin {
		t.Fatalf("mainnet post v5.5: %d", got)
	}
	if got := BlockSubsidySats(false, 1_153_161); got != 5*coin {
		t.Fatalf("mainnet post zc v2: %d", got)
	}
	if BlockSubsidySats(false, 0) != 0 {
		t.Fatal("height 0")
	}
}

func TestLegacySignatureMatchesBitcoinV1(t *testing.T) {
	key, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		t.Fatal(err)
	}
	pub := key.PubKey().SerializeCompressed()
	addr, err := btcutil.NewAddressPubKeyHash(btcutil.Hash160(pub), RegressionNetParams())
	if err != nil {
		t.Fatal(err)
	}
	pkScript, err := btctxscript.PayToAddrScript(addr)
	if err != nil {
		t.Fatal(err)
	}
	prevHash, err := chainhash.NewHashFromStr("1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	tx := wire.NewMsgTx(1)
	tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(prevHash, 0), nil, nil))
	dest, err := btcutil.NewAddressPubKeyHash(bytes.Repeat([]byte{0x33}, 20), RegressionNetParams())
	if err != nil {
		t.Fatal(err)
	}
	destScript, err := btctxscript.PayToAddrScript(dest)
	if err != nil {
		t.Fatal(err)
	}
	tx.AddTxOut(wire.NewTxOut(50_000, destScript))
	tx.AddTxOut(wire.NewTxOut(40_000, pkScript))

	prev := []*wire.TxOut{wire.NewTxOut(100_000, pkScript)}
	hashes, err := SignatureHashes(tx, prev)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := key.Sign(hashes[0])
	if err != nil {
		t.Fatal(err)
	}
	script, err := SignatureScript(hashes[0], sig, key.PubKey())
	if err != nil {
		t.Fatal(err)
	}
	tx.TxIn[0].SignatureScript = script
	if err = VerifyTransaction(tx, prev); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err = tx.Serialize(&buf); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	if !bytes.Equal(raw[:4], []byte{0x01, 0x00, 0x00, 0x00}) {
		t.Fatalf("serialized header %x, want version 1 type 0", raw[:4])
	}
	version, txType, ok := TxVersionAndType(hex.EncodeToString(raw))
	if !ok || version != 1 || txType != 0 {
		t.Fatalf("parsed version %d type %d ok %v", version, txType, ok)
	}
	tx.Version = 2
	if _, err = SignatureHashes(tx, prev); err == nil {
		t.Fatal("signed a non-v1 transaction")
	}
}

func TestSkipSpecialAndShielded(t *testing.T) {
	proreg := &btcjson.TxRawResult{Hex: "030001000000000000000000"}
	if !SkipTx(proreg) {
		t.Fatal("kept a type-1 special transaction")
	}
	shielded := &btcjson.TxRawResult{Hex: "030000000000000000000000"}
	if !SkipTx(shielded) {
		t.Fatal("kept a sapling tx with no transparent input")
	}
	plain := &btcjson.TxRawResult{
		Hex: "0100000001" + hex.EncodeToString(bytes.Repeat([]byte{0xab}, 32)) + "0000000000ffffffff00",
		Vin: []btcjson.Vin{{Txid: "aa"}},
		Vout: []btcjson.Vout{{
			Value:        1,
			ScriptPubKey: btcjson.ScriptPubKeyResult{Hex: "76a914" + hex.EncodeToString(bytes.Repeat([]byte{1}, 20)) + "88ac"},
		}},
	}
	if SkipTx(plain) {
		t.Fatal("skipped a transparent v1 payment")
	}
	coinstake := &btcjson.TxRawResult{
		Vin: []btcjson.Vin{{Txid: "bb"}},
		Vout: []btcjson.Vout{
			{Value: 0, ScriptPubKey: btcjson.ScriptPubKeyResult{Hex: ""}},
			{Value: 250, ScriptPubKey: btcjson.ScriptPubKeyResult{Hex: "76a91400"}},
		},
	}
	if !SkipTx(coinstake) {
		t.Fatal("kept a coinstake")
	}
	if !IsRejectedOutputScript("76a97b63d214" + hex.EncodeToString(bytes.Repeat([]byte{1}, 20))) {
		t.Fatal("missed OP_CHECKCOLDSTAKEVERIFY")
	}
	if !IsRejectedOutputScript("e076a914" + hex.EncodeToString(bytes.Repeat([]byte{1}, 20))) {
		t.Fatal("missed OP_EXCHANGEADDR")
	}
	if IsRejectedOutputScript("76a914" + hex.EncodeToString(bytes.Repeat([]byte{1}, 20)) + "88ac") {
		t.Fatal("rejected a P2PKH script")
	}
	// 0xd2 inside the pubkey hash must not be read as OP_CHECKCOLDSTAKEVERIFY.
	hash := bytes.Repeat([]byte{0xd2}, 20)
	if IsRejectedOutputScript("76a914" + hex.EncodeToString(hash) + "88ac") {
		t.Fatal("rejected a P2PKH script whose hash contains 0xd2")
	}
}

func base58Check(body []byte) string {
	sum := sha256.Sum256(body)
	sum = sha256.Sum256(sum[:])
	return base58.Encode(append(body, sum[:4]...))
}
