package thorclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"

	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	gateway "github.com/cosmos/gogogateway"
	. "gopkg.in/check.v1"

	bifrosttypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	thornodetypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// KeysignParitySuite pins the keysign wire contract between THORNode and
// bifrost. THORNode's queryKeysign signs json.Marshal of its own TxOut;
// bifrost's GetKeysign decodes the response into bifrost's TxOut, marshals
// that again and verifies the signature over the result. Any field THORNode
// emits that bifrost drops, renames, reorders or re-encodes changes the bytes,
// and every keysign block for the vault then fails with "bad signature"
// (!4342 removed a field from bifrost's TxArrayItem and stalled all outbounds).
//
// The THORNode TxOut is filled by reflection so a field added on either side
// is covered without editing this test.
type KeysignParitySuite struct{}

var _ = Suite(&KeysignParitySuite{})

const keysignParityHeight = int64(1024)

// keysignNeverEmptyFields are TxOutItem fields a scheduled outbound always
// carries: prepareTxOutItem rejects an empty to_address and one that is not on
// toi.Chain. THORNode tags both without omitempty and bifrost with it, so only
// their zero values would fail to round-trip.
var keysignNeverEmptyFields = map[string]bool{"Chain": true, "ToAddress": true}

// TestKeysignBytesSurviveBifrostRoundTrip checks the exact bytes THORNode
// signs come back unchanged from bifrost's decode/re-encode.
func (s *KeysignParitySuite) TestKeysignBytesSurviveBifrostRoundTrip(c *C) {
	txOut := fullyPopulatedKeysignTxOut(c)
	signed, err := json.Marshal(txOut) // as queryKeysign
	c.Assert(err, IsNil)
	if msg := keysignRoundTripMismatch(c, signed, "every field set"); msg != "" {
		c.Fatal(msg)
	}
}

// TestKeysignZeroFieldsSurviveBifrostRoundTrip zeroes one TxOutItem field at
// a time, so an omitempty mismatch on a field that is legitimately empty in
// production (memo on a memoless outbound, no aggregator, ...) is caught too.
func (s *KeysignParitySuite) TestKeysignZeroFieldsSurviveBifrostRoundTrip(c *C) {
	itemType := reflect.TypeOf(thornodetypes.TxOutItem{})
	for i := 0; i < itemType.NumField(); i++ {
		field := itemType.Field(i)
		if !field.IsExported() || keysignNeverEmptyFields[field.Name] {
			continue
		}
		txOut := fullyPopulatedKeysignTxOut(c)
		item := reflect.ValueOf(&txOut.TxArray[0]).Elem()
		item.Field(i).Set(reflect.Zero(field.Type))
		signed, err := json.Marshal(txOut)
		c.Assert(err, IsNil)
		if msg := keysignRoundTripMismatch(c, signed, "TxOutItem."+field.Name+" zero"); msg != "" {
			c.Error(msg)
		}
	}
}

// TestGetKeysignVerifiesThornodeSignature serves a keysign response built the
// way THORNode builds it and runs bifrost's real GetKeysign, including its
// signature verification.
func (s *KeysignParitySuite) TestGetKeysignVerifiesThornodeSignature(c *C) {
	cfg, _, kb := SetupThorchainForTest(c)
	txOut := fullyPopulatedKeysignTxOut(c)
	body := thornodeKeysignResponseBody(c, kb, cfg.SignerName, txOut)

	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if !strings.HasPrefix(req.RequestURI, KeysignEndpoint) {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		_, err := rw.Write(body)
		c.Check(err, IsNil)
	}))
	defer server.Close()

	cfg.ChainHost = server.Listener.Addr().String()
	b, err := NewThorchainBridge(cfg, GetMetricForTest(c), NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd))
	c.Assert(err, IsNil)
	bridge, ok := b.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	bridge.httpClient.RetryMax = 1

	keysign, err := bridge.GetKeysign(keysignParityHeight, txOut.TxArray[0].VaultPubKey.String())
	c.Assert(err, IsNil, Commentf("bifrost rejected a keysign block THORNode signed"))
	c.Assert(keysign.Height, Equals, keysignParityHeight)
	c.Assert(keysign.TxArray, HasLen, len(txOut.TxArray))
	c.Assert(keysign.TxArray[0].VaultPubKey.Equals(txOut.TxArray[0].VaultPubKey), Equals, true)
}

// thornodeKeysignResponseBody mirrors queryKeysign (sign json.Marshal of the
// TxOut with the node key in SIGN_MODE_LEGACY_AMINO_JSON) and the REST
// gateway marshaler CustomGRPCGatewayRouter installs.
func thornodeKeysignResponseBody(c *C, kb keyring.Keyring, signer string, txOut *thornodetypes.TxOut) []byte {
	buf, err := json.Marshal(txOut)
	c.Assert(err, IsNil)
	sig, _, err := kb.Sign(signer, buf, signing.SignMode_SIGN_MODE_LEGACY_AMINO_JSON)
	c.Assert(err, IsNil)
	resp := &thornodetypes.QueryKeysignResponse{
		Keysign:   txOut,
		Signature: base64.StdEncoding.EncodeToString(sig),
	}
	marshaler := &gateway.JSONPb{EmitDefaults: true, OrigName: true}
	body, err := marshaler.Marshal(resp)
	c.Assert(err, IsNil)
	return body
}

// keysignRoundTripMismatch decodes the bytes THORNode signs into bifrost's
// TxOut and re-marshals them as GetKeysign does. It returns "" when the bytes
// are identical, and otherwise a message naming each divergent JSON path.
func keysignRoundTripMismatch(c *C, signed []byte, scenario string) string {
	var decoded bifrosttypes.TxOut
	c.Assert(json.Unmarshal(signed, &decoded), IsNil, Commentf("%s: bifrost cannot decode THORNode keysign JSON", scenario))
	remarshalled, err := json.Marshal(decoded)
	c.Assert(err, IsNil)
	if bytes.Equal(signed, remarshalled) {
		return ""
	}

	diffs := keysignJSONDiff(c, signed, remarshalled)
	if len(diffs) == 0 {
		diffs = []string{"same fields and values but different encoding (field order?)"}
	}
	return fmt.Sprintf("%s: bifrost's TxOut does not reproduce the bytes THORNode signs, "+
		"so GetKeysign would reject every keysign block with \"bad signature\".\n"+
		"Keep bifrost/thorclient/types.TxOut/TxArrayItem in step with x/thorchain/types.TxOut/TxOutItem:\n  %s\n"+
		"THORNode signs:  %s\nbifrost verifies: %s",
		scenario, strings.Join(diffs, "\n  "), signed, remarshalled)
}

// keysignJSONDiff lists the JSON paths whose presence or value differs.
func keysignJSONDiff(c *C, thornode, bifrost []byte) []string {
	decode := func(raw []byte) any {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		c.Assert(dec.Decode(&v), IsNil)
		return v
	}
	var diffs []string
	var walk func(path string, want, got any)
	walk = func(path string, want, got any) {
		switch w := want.(type) {
		case map[string]any:
			g, ok := got.(map[string]any)
			if !ok {
				diffs = append(diffs, fmt.Sprintf("%s: THORNode sends an object, bifrost sends %v", path, got))
				return
			}
			keys := make([]string, 0, len(w)+len(g))
			for k := range w {
				keys = append(keys, k)
			}
			for k := range g {
				if _, ok := w[k]; !ok {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			for _, k := range keys {
				wv, inWant := w[k]
				gv, inGot := g[k]
				switch {
				case !inGot:
					diffs = append(diffs, fmt.Sprintf("%s.%s: signed by THORNode but dropped by bifrost", path, k))
				case !inWant:
					diffs = append(diffs, fmt.Sprintf("%s.%s: added by bifrost but not signed by THORNode", path, k))
				default:
					walk(path+"."+k, wv, gv)
				}
			}
		case []any:
			g, ok := got.([]any)
			if !ok || len(g) != len(w) {
				diffs = append(diffs, fmt.Sprintf("%s: THORNode sends %v, bifrost sends %v", path, want, got))
				return
			}
			for i := range w {
				walk(fmt.Sprintf("%s[%d]", path, i), w[i], g[i])
			}
		default:
			if !reflect.DeepEqual(want, got) {
				diffs = append(diffs, fmt.Sprintf("%s: THORNode sends %v, bifrost sends %v", path, want, got))
			}
		}
	}
	walk("keysign", decode(thornode), decode(bifrost))
	return diffs
}

// fullyPopulatedKeysignTxOut returns a THORNode TxOut whose every exported
// field, nested ones included, holds a non-zero value.
func fullyPopulatedKeysignTxOut(c *C) *thornodetypes.TxOut {
	txOut := &thornodetypes.TxOut{}
	f := &keysignFiller{}
	if err := f.fill(reflect.ValueOf(txOut).Elem(), "TxOut"); err != nil {
		c.Fatal(err)
	}
	txOut.Height = keysignParityHeight
	c.Assert(txOut.TxArray, Not(HasLen), 0)
	return txOut
}

// keysignFiller sets every exported field to a distinct non-zero value.
// Types with custom JSON encoding only accept well-formed values, so they get
// a realistic sample from samples instead of a synthetic one.
type keysignFiller struct {
	seq int64
}

var (
	keysignJSONMarshaler   = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	keysignJSONUnmarshaler = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
)

func (f *keysignFiller) sample(t reflect.Type) (reflect.Value, bool) {
	var v any
	switch t {
	case reflect.TypeOf(common.Asset{}):
		v = common.ETHAsset
	case reflect.TypeOf(common.PubKey("")):
		v = thornodetypes.GetRandomPubKey()
	case reflect.TypeOf(common.Chain("")):
		v = common.ETHChain
	case reflect.TypeOf(common.Address("")):
		v = thornodetypes.GetRandomETHAddress()
	case reflect.TypeOf(common.TxID("")):
		v = thornodetypes.GetRandomTxHash()
	case reflect.TypeOf(cosmos.Uint{}):
		v = cosmos.NewUint(uint64(f.next()))
	default:
		return reflect.Value{}, false
	}
	return reflect.ValueOf(v), true
}

func (f *keysignFiller) next() int64 {
	f.seq++
	return f.seq
}

func (f *keysignFiller) fill(v reflect.Value, path string) error {
	if v.Kind() == reflect.Pointer {
		v.Set(reflect.New(v.Type().Elem()))
		return f.fill(v.Elem(), path)
	}
	if sample, ok := f.sample(v.Type()); ok {
		v.Set(sample)
		return nil
	}
	if v.Type().Implements(keysignJSONMarshaler) || reflect.PointerTo(v.Type()).Implements(keysignJSONUnmarshaler) {
		return fmt.Errorf("%s: %s has custom JSON encoding; add a well-formed sample to keysignFiller.sample", path, v.Type())
	}

	switch v.Kind() {
	case reflect.String:
		v.SetString(fmt.Sprintf("%s-%d", path, f.next()))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(f.next())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(f.next()))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(f.next()) + 0.5)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		return f.fill(v.Index(0), path+"[0]")
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := f.fill(v.Index(i), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		key := reflect.New(v.Type().Key()).Elem()
		if err := f.fill(key, path+"{key}"); err != nil {
			return err
		}
		val := reflect.New(v.Type().Elem()).Elem()
		if err := f.fill(val, path+"{value}"); err != nil {
			return err
		}
		v.Set(reflect.MakeMap(v.Type()))
		v.SetMapIndex(key, val)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			if err := f.fill(v.Field(i), path+"."+field.Name); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%s: no filler for kind %s", path, v.Kind())
	}

	if v.IsZero() {
		return fmt.Errorf("%s: filler left %s zero", path, v.Type())
	}
	return nil
}
