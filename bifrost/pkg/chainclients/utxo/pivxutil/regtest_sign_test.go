package pivxutil

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil/base58"
	btctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"
)

// TestRegtestSignAndBroadcast builds a legacy v1 transaction with the same
// digest, scriptSig and serializer the bifrost signer uses, then submits it
// with sendrawtransaction. pivxd 5.6.1 has no testmempoolaccept.
//
// Set PIVX_REGTEST_URL, for example http://thorchain:password@127.0.0.1:51473.
// Set PIVX_FIXTURE_DIR to also write the accepted transaction JSON.
func TestRegtestSignAndBroadcast(t *testing.T) {
	rawURL := os.Getenv("PIVX_REGTEST_URL")
	if rawURL == "" {
		t.Skip("set PIVX_REGTEST_URL to a pivxd regtest RPC")
	}
	rpc := newRegtestRPC(t, rawURL)

	var unspent []struct {
		TxID         string  `json:"txid"`
		Vout         uint32  `json:"vout"`
		Amount       float64 `json:"amount"`
		ScriptPubKey string  `json:"scriptPubKey"`
		Address      string  `json:"address"`
	}
	rpc.call(t, "listunspent", []any{1, 9999999}, &unspent)
	if len(unspent) == 0 {
		t.Fatal("no spendable outputs; generate mature coinbases first")
	}
	in := unspent[0]
	var wif string
	rpc.call(t, "dumpprivkey", []any{in.Address}, &wif)
	key := keyFromWIF(t, wif)

	var dest string
	rpc.call(t, "getnewaddress", []any{}, &dest)
	destAddr, err := DecodeAddress(dest, RegressionNetParams())
	if err != nil {
		t.Fatal(err)
	}
	changeAddr, err := DecodeAddress(in.Address, RegressionNetParams())
	if err != nil {
		t.Fatal(err)
	}
	destScript, err := btctxscript.PayToAddrScript(destAddr)
	if err != nil {
		t.Fatal(err)
	}
	changeScript, err := btctxscript.PayToAddrScript(changeAddr)
	if err != nil {
		t.Fatal(err)
	}
	prevScript, err := hex.DecodeString(in.ScriptPubKey)
	if err != nil {
		t.Fatal(err)
	}
	prevHash, err := chainhash.NewHashFromStr(in.TxID)
	if err != nil {
		t.Fatal(err)
	}
	inputSats, err := amountToSats(in.Amount)
	if err != nil {
		t.Fatal(err)
	}

	tx := wire.NewMsgTx(wire.TxVersion)
	tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(prevHash, in.Vout), nil, nil))
	const pay int64 = 100_000_000
	const fee int64 = 10_000
	change := inputSats - pay - fee
	if change <= 0 {
		t.Fatalf("input %d cannot pay %d plus fee", inputSats, pay)
	}
	tx.AddTxOut(wire.NewTxOut(pay, destScript))
	tx.AddTxOut(wire.NewTxOut(change, changeScript))

	prevOuts := []*wire.TxOut{wire.NewTxOut(inputSats, prevScript)}
	hashes, err := SignatureHashes(tx, prevOuts)
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
	if err = VerifyTransaction(tx, prevOuts); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err = tx.Serialize(&buf); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	if !bytes.Equal(raw[:4], []byte{0x01, 0x00, 0x00, 0x00}) {
		t.Fatalf("header %x", raw[:4])
	}
	hexTx := hex.EncodeToString(raw)

	var txid string
	rpc.call(t, "sendrawtransaction", []any{hexTx, true}, &txid)
	if txid != tx.TxHash().String() {
		t.Fatalf("txid %s, local hash %s", txid, tx.TxHash())
	}
	var verbose struct {
		Version int    `json:"version"`
		Type    int    `json:"type"`
		TxID    string `json:"txid"`
		Hex     string `json:"hex"`
	}
	rpc.call(t, "getrawtransaction", []any{txid, true}, &verbose)
	if verbose.Version != 1 || verbose.Type != 0 || verbose.Hex != hexTx {
		t.Fatalf("node decoded %+v", verbose)
	}
	t.Logf("pivxd accepted %s version=%d type=%d bytes=%d", txid, verbose.Version, verbose.Type, len(raw))

	if dir := os.Getenv("PIVX_FIXTURE_DIR"); dir != "" {
		writeFixture(t, dir, "signed_tx.json", map[string]any{
			"txid": txid, "hex": hexTx, "version": verbose.Version, "type": verbose.Type,
		})
	}
}

func amountToSats(v float64) (int64, error) {
	// Whole-coin regtest values are exact in float64 up to 2^53 / 1e8.
	sats := int64(v*1e8 + 0.5)
	if float64(sats)/1e8 != v {
		return 0, fmt.Errorf("amount %v is not an exact satoshi quantity", v)
	}
	return sats, nil
}

func keyFromWIF(t *testing.T, wif string) *btcec.PrivateKey {
	t.Helper()
	payload, version, err := base58.CheckDecode(wif)
	if err != nil {
		t.Fatal(err)
	}
	if version != RegTestSecretKeyID {
		t.Fatalf("WIF version %d", version)
	}
	if len(payload) == 33 && payload[32] == 1 {
		payload = payload[:32]
	}
	if len(payload) != 32 {
		t.Fatalf("private key length %d", len(payload))
	}
	key, _ := btcec.PrivKeyFromBytes(btcec.S256(), payload)
	return key
}

func writeFixture(t *testing.T, dir, name string, v any) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, name), append(buf, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

type regtestRPC struct {
	url  string
	auth string
	t    *testing.T
}

func newRegtestRPC(t *testing.T, rawURL string) *regtestRPC {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	user := ""
	pass := ""
	if parsed.User != nil {
		user = parsed.User.Username()
		pass, _ = parsed.User.Password()
		parsed.User = nil
	}
	return &regtestRPC{
		url:  parsed.String(),
		auth: base64.StdEncoding.EncodeToString([]byte(user + ":" + pass)),
		t:    t,
	}
}

func (r *regtestRPC) call(t *testing.T, method string, params []any, out any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "1.0",
		"id":      "pivx",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Basic "+r.auth)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err = json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("%s: %v (%s)", method, err, payload)
	}
	if envelope.Error != nil {
		t.Fatalf("%s rpc %d: %s", method, envelope.Error.Code, envelope.Error.Message)
	}
	if out != nil && len(envelope.Result) > 0 && string(envelope.Result) != "null" {
		if err = json.Unmarshal(envelope.Result, out); err != nil {
			t.Fatalf("%s decode: %v", method, err)
		}
	}
}
