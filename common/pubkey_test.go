package common

import (
	"encoding/json"
	"fmt"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type KeyDataAddr struct {
	mainnet string
	mocknet string
}

type KeyData struct {
	priv     string
	pub      string
	addrBTC  KeyDataAddr
	addrLTC  KeyDataAddr
	addrBCH  KeyDataAddr
	addrETH  KeyDataAddr
	addrDOGE KeyDataAddr
	addrSOL  KeyDataAddr
}

type PubKeyTestSuite struct {
	keyData []KeyData
}

var _ = Suite(&PubKeyTestSuite{})

func (s *PubKeyTestSuite) SetUpSuite(_ *C) {
	s.keyData = []KeyData{
		{
			priv: "ef235aacf90d9f4aadd8c92e4b2562e1d9eb97f0df9ba3b508258739cb013db2",
			pub:  "02b4632d08485ff1df2db55b9dafd23347d1c47a457072a1e87be26896549a8737",
			addrETH: KeyDataAddr{
				mainnet: "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
				mocknet: "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473",
			},
			addrBTC: KeyDataAddr{
				mainnet: "bc1qj08ys4ct2hzzc2hcz6h2hgrvlmsjynawlht528",
				mocknet: "bcrt1qj08ys4ct2hzzc2hcz6h2hgrvlmsjynawhcf2xa",
			},
			addrLTC: KeyDataAddr{
				mainnet: "ltc1qj08ys4ct2hzzc2hcz6h2hgrvlmsjynawmt3sjh",
				mocknet: "rltc1qj08ys4ct2hzzc2hcz6h2hgrvlmsjynawf4nr3r",
			},
			addrBCH: KeyDataAddr{
				mainnet: "qzfuujzhpd2ugtp2lqt2a2aqdnlwzgj04cswjhml4x",
				mocknet: "qzfuujzhpd2ugtp2lqt2a2aqdnlwzgj04cwqq36m3u",
			},
			addrDOGE: KeyDataAddr{
				mainnet: "DJcczDr7oNvfj5qP17Qa7p9ZUNTfnYYDJC",
				mocknet: "mtzUk1zTJzTdyC8Pz6PPPyCHTEL5RLVyDJ",
			},
			addrSOL: KeyDataAddr{
				mainnet: "DhMcGoLSWsKLqoM12GhR1ZxgcRFzZo2wZ8D2A3BvX9op",
				mocknet: "DhMcGoLSWsKLqoM12GhR1ZxgcRFzZo2wZ8D2A3BvX9op",
			},
		},
		{
			priv: "289c2857d4598e37fb9647507e47a309d6133539bf21a8b9cb6df88fd5232032",
			pub:  "037db227d7094ce215c3a0f57e1bcc732551fe351f94249471934567e0f5dc1bf7",
			addrETH: KeyDataAddr{
				mainnet: "0x970e8128ab834e8eac17ab8e3812f010678cf791",
				mocknet: "0x970e8128ab834e8eac17ab8e3812f010678cf791",
			},
			addrBTC: KeyDataAddr{
				mainnet: "bc1qzupk5lmc84r2dh738a9g3zscavannjy38ghlxu",
				mocknet: "bcrt1qzupk5lmc84r2dh738a9g3zscavannjy3084p2x",
			},
			addrLTC: KeyDataAddr{
				mainnet: "ltc1qzupk5lmc84r2dh738a9g3zscavannjy3r5dm7v",
				mocknet: "rltc1qzupk5lmc84r2dh738a9g3zscavannjy3320gac",
			},
			addrBCH: KeyDataAddr{
				mainnet: "qqtsx6nl0q75dfkl6yl54zy2rr4nkwwgjyzgwf5228",
				mocknet: "qqtsx6nl0q75dfkl6yl54zy2rr4nkwwgjyuxu04wwa",
			},
			addrDOGE: KeyDataAddr{
				mainnet: "D7EnB23qxiWTBGD1z9N6Ui7VXonCgY9eeE",
				mocknet: "mhcdvpCBUL3RRNW2y8LuksADWfecEmkzju",
			},
			addrSOL: KeyDataAddr{
				mainnet: "G8n9W2tGpcazbyrdQzniSKV6p4uFJArWCENhapDmHTx9",
				mocknet: "G8n9W2tGpcazbyrdQzniSKV6p4uFJArWCENhapDmHTx9",
			},
		},
		{
			priv: "e810f1d7d6691b4a7a73476f3543bd87d601f9a53e7faf670eac2c5b517d83bf",
			pub:  "03f98464e8d3fc8e275e34c6f8dc9b99aa244e37b0d695d0dfb8884712ed6d4d35",
			addrETH: KeyDataAddr{
				mainnet: "0xf6da288748ec4c77642f6c5543717539b3ae001b",
				mocknet: "0xf6da288748ec4c77642f6c5543717539b3ae001b",
			},
			addrBTC: KeyDataAddr{
				mainnet: "bc1qqqnde7kqe5sf96j6zf8jpzwr44dh4gkdek4rvd",
				mocknet: "bcrt1qqqnde7kqe5sf96j6zf8jpzwr44dh4gkd3ehaqh",
			},
			addrLTC: KeyDataAddr{
				mainnet: "ltc1qqqnde7kqe5sf96j6zf8jpzwr44dh4gkda2085a",
				mocknet: "rltc1qqqnde7kqe5sf96j6zf8jpzwr44dh4gkd05d5hf",
			},
			addrBCH: KeyDataAddr{
				mainnet: "qqqzdh86crxjpyh2tgfy7gyfcwk4k74ze522f8panm",
				mocknet: "qqqzdh86crxjpyh2tgfy7gyfcwk4k74ze55ympqehp",
			},
			addrDOGE: KeyDataAddr{
				mainnet: "D59u6XAtQCybdvFB4ZLWH4h8cK5SY8show",
				mocknet: "mfXkrKKDupWZt2YC3YKKZDjrbAwrBFwj8W",
			},
			addrSOL: KeyDataAddr{
				mainnet: "49gTXamRza4famcZDvFve6omcFqpiooaWBTX2KK3kp4h",
				mocknet: "49gTXamRza4famcZDvFve6omcFqpiooaWBTX2KK3kp4h",
			},
		},
		{
			priv: "a96e62ed3955e65be32703f12d87b6b5cf26039ecfa948dc5107a495418e5330",
			pub:  "02950e1cdfcb133d6024109fd489f734eeb4502418e538c28481f22bce276f248c",
			addrETH: KeyDataAddr{
				mainnet: "0xfabb9cc6ec839b1214bb11c53377a56a6ed81762",
				mocknet: "0xfabb9cc6ec839b1214bb11c53377a56a6ed81762",
			},
			addrBTC: KeyDataAddr{
				mainnet: "bc1q0s4mg25tu6termrk8egltfyme4q7sg3h0e56p3",
				mocknet: "bcrt1q0s4mg25tu6termrk8egltfyme4q7sg3h8kkydt",
			},
			addrLTC: KeyDataAddr{
				mainnet: "ltc1q0s4mg25tu6termrk8egltfyme4q7sg3ht9w7ep",
				mocknet: "rltc1q0s4mg25tu6termrk8egltfyme4q7sg3hemvd64",
			},
			addrBCH: KeyDataAddr{
				mainnet: "qp7zhdp230nf0y0vwcl9radyn0x5r6pzxuqvhx5vs3",
				mocknet: "qp7zhdp230nf0y0vwcl9radyn0x5r6pzxu7z9q4g5t",
			},
			addrDOGE: KeyDataAddr{
				mainnet: "DGTegdtiJ6Y9fteiWtHNS5bpjCJSrY4Kiz",
				mocknet: "mrqWSS33oi57uzwjVsGBiEeYi4ArRRWHV4",
			},
			addrSOL: KeyDataAddr{
				mainnet: "C7nGYnw9qTT4XjYKiynAF3Qz9iPrZfXg338sdvBGdBhS",
				mocknet: "C7nGYnw9qTT4XjYKiynAF3Qz9iPrZfXg338sdvBGdBhS",
			},
		},
		{
			priv: "9294f4d108465fd293f7fe299e6923ef71a77f2cb1eb6d4394839c64ec25d5c0",
			pub:  "0238383ee4d60176d27cf46f0863bfc6aea624fe9bfc7f4273cc5136d9eb483e4a",
			addrETH: KeyDataAddr{
				mainnet: "0x1f30a82340f08177aba70e6f48054917c74d7d38",
				mocknet: "0x1f30a82340f08177aba70e6f48054917c74d7d38",
			},
			addrBTC: KeyDataAddr{
				mainnet: "bc1qjw8h4l3dtz5xxc7uyh5ys70qkezspgfurtjs2p",
				mocknet: "bcrt1qjw8h4l3dtz5xxc7uyh5ys70qkezspgfutyswxm",
			},
			addrLTC: KeyDataAddr{
				mainnet: "ltc1qjw8h4l3dtz5xxc7uyh5ys70qkezspgfu8hg5j3",
				mocknet: "rltc1qjw8h4l3dtz5xxc7uyh5ys70qkezspgfu4f2839",
			},
			addrBCH: KeyDataAddr{
				mainnet: "qzfc77h794v2scmrmsj7sjreuzmy2q9p8sxs8lu34e",
				mocknet: "qzfc77h794v2scmrmsj7sjreuzmy2q9p8sc74ea43r",
			},
			addrDOGE: KeyDataAddr{
				mainnet: "DJbKker23xfz3ufxAbqUuQwp1EBibGJJHu",
				mocknet: "mtyBWSzMZaCxJ1xy9apJBZzXz648BZrpJg",
			},
			addrSOL: KeyDataAddr{
				mainnet: "GYnVuwPsSAE6QwWh6TebBqNbFFqsU2563eShGcPbAvL2",
				mocknet: "GYnVuwPsSAE6QwWh6TebBqNbFFqsU2563eShGcPbAvL2",
			},
		},
	}
}

// TestPubKey implementation
func (s *PubKeyTestSuite) TestPubKey(c *C) {
	_, pubKey, _ := testdata.KeyTestPubAddr()
	spk, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, pubKey)
	c.Assert(err, IsNil)
	pk, err := NewPubKey(spk)
	c.Assert(err, IsNil)
	hexStr := pk.String()
	c.Assert(len(hexStr) > 0, Equals, true)
	pk1, err := NewPubKey(hexStr)
	c.Assert(err, IsNil)
	c.Assert(pk.Equals(pk1), Equals, true)

	result, err := json.Marshal(pk)
	c.Assert(err, IsNil)
	c.Log(result, Equals, fmt.Sprintf(`"%s"`, hexStr))
	var pk2 PubKey
	err = json.Unmarshal(result, &pk2)
	c.Assert(err, IsNil)
	c.Assert(pk2.Equals(pk), Equals, true)
}

func (s *PubKeyTestSuite) TestDecodePubKey(c *C) {
	_, secpKey, _ := testdata.KeyTestPubAddr()
	_, edKey, _ := KeyTestPubAddrED25519()
	for _, want := range []cryptotypes.PubKey{secpKey, edKey} {
		encoded, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, want)
		c.Assert(err, IsNil)
		key, err := NewPubKey(encoded)
		c.Assert(err, IsNil)

		fromString, err := DecodePubKey(encoded)
		c.Assert(err, IsNil)
		fromKey, err := key.Decode()
		c.Assert(err, IsNil)
		for _, got := range []cryptotypes.PubKey{fromString, fromKey} {
			c.Assert(got.Type(), Equals, want.Type())
			c.Assert(got.Bytes(), DeepEquals, want.Bytes())
			c.Assert(got.Equals(want), Equals, true)
		}
	}
}

func (s *PubKeyTestSuite) TestDecodePubKeyInvalid(c *C) {
	_, pubKey, _ := testdata.KeyTestPubAddr()
	encoded, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, pubKey)
	c.Assert(err, IsNil)
	consensusKey, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeConsPub, pubKey)
	c.Assert(err, IsNil)
	payload, err := cosmos.GetFromBech32(encoded, cosmos.GetConfig().GetBech32AccountPubPrefix())
	c.Assert(err, IsNil)
	wrongPrefix, err := ConvertAndEncode("wrongpub", payload)
	c.Assert(err, IsNil)
	invalidPayload, err := ConvertAndEncode(cosmos.GetConfig().GetBech32AccountPubPrefix(), []byte{1, 2, 3})
	c.Assert(err, IsNil)
	// Change one checksum character while retaining the valid Bech32 alphabet.
	last := byte('q')
	if encoded[len(encoded)-1] == last {
		last = 'p'
	}
	badChecksum := encoded[:len(encoded)-1] + string(last)

	for _, invalid := range []string{"", "invalid", consensusKey, wrongPrefix, invalidPayload, badChecksum} {
		_, err = DecodePubKey(invalid)
		c.Assert(err, NotNil, Commentf("accepted encoded key %q", invalid))
		_, err = PubKey(invalid).Decode()
		c.Assert(err, NotNil, Commentf("accepted PubKey %q", invalid))
	}

	// Construction still permits the empty sentinel; decoding requires a key.
	empty, err := NewPubKey("")
	c.Assert(err, IsNil)
	c.Assert(empty, Equals, EmptyPubKey)
	_, err = empty.Decode()
	c.Assert(err, NotNil)
}

func (s *PubKeyTestSuite) TestPubKeySet(c *C) {
	_, pubKey, _ := testdata.KeyTestPubAddr()
	spk, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, pubKey)
	c.Assert(err, IsNil)
	pk, err := NewPubKey(spk)
	c.Assert(err, IsNil)

	c.Check(PubKeySet{}.Contains(pk), Equals, false)

	pks := PubKeySet{
		Secp256k1: pk,
	}
	c.Check(pks.Contains(pk), Equals, true)
	pks = PubKeySet{
		Ed25519: pk,
	}
	c.Check(pks.Contains(pk), Equals, true)
}

func (s *PubKeyTestSuite) TestEquals(c *C) {
	var pk1, pk2, pk3, pk4 PubKey
	_, pubKey1, _ := testdata.KeyTestPubAddr()
	tpk1, err1 := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, pubKey1)
	c.Assert(err1, IsNil)
	pk1 = PubKey(tpk1)

	_, pubKey2, _ := testdata.KeyTestPubAddr()
	tpk2, err2 := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, pubKey2)
	c.Assert(err2, IsNil)
	pk2 = PubKey(tpk2)

	_, pubKey3, _ := testdata.KeyTestPubAddr()
	tpk3, err3 := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, pubKey3)
	c.Assert(err3, IsNil)
	pk3 = PubKey(tpk3)

	_, pubKey4, _ := testdata.KeyTestPubAddr()
	tpk4, err4 := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, pubKey4)
	c.Assert(err4, IsNil)
	pk4 = PubKey(tpk4)

	c.Assert(PubKeys{
		pk1, pk2,
	}.Equals(nil), Equals, false)

	c.Assert(PubKeys{
		pk1, pk2, pk3,
	}.Equals(PubKeys{
		pk1, pk2,
	}), Equals, false)

	c.Assert(PubKeys{
		pk1, pk2, pk3, pk4,
	}.Equals(PubKeys{
		pk4, pk3, pk2, pk1,
	}), Equals, true)

	c.Assert(PubKeys{ // nolint
		pk1, pk2, pk3, pk4,
	}.Equals(PubKeys{
		pk1, pk2, pk3, pk4,
	}), Equals, true)
}

// Borrowed from https://github.com/cosmos/cosmos-sdk/blob/v0.52.0-beta.2/testutil/testdata/tx.go#L59
func KeyTestPubAddrED25519() (cryptotypes.PrivKey, cryptotypes.PubKey, sdk.AccAddress) {
	key := ed25519.GenPrivKey()
	pub := key.PubKey()
	addr := sdk.AccAddress(pub.Address())
	return key, pub, addr
}

func (s *PubKeyTestSuite) TestSOLPubKey(c *C) {
	/** TODO: Replace with testdata.KeyTestPubAddrED25519() when cosmos v0.52 lands **/
	_, pubKey1, _ := KeyTestPubAddrED25519()
	spk, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, pubKey1)
	c.Assert(err, IsNil)
	pk, err := NewPubKey(spk)
	c.Assert(err, IsNil)
	hexStr := pk.String()
	c.Assert(len(hexStr) > 0, Equals, true)
	pk1, err := NewPubKey(hexStr)
	c.Assert(err, IsNil)
	c.Assert(pk.Equals(pk1), Equals, true)

	result, err := json.Marshal(pk)
	c.Assert(err, IsNil)
	c.Log(result, Equals, fmt.Sprintf(`"%s"`, hexStr))
	var pk2 PubKey
	err = json.Unmarshal(result, &pk2)
	c.Assert(err, IsNil)
	c.Assert(pk2.Equals(pk), Equals, true)
}

func (s *PubKeyTestSuite) TestNewPubKeyValidationCache(c *C) {
	valid := "tthorpub1addwnpepqt7qug8vk9r3saw8n4r803ydj2g3dqwx0mvq5akhnze86fc536xcycgtrnv"
	_, ref := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeAccPub, valid)
	for i := 0; i < 2; i++ { // second pass is served from the cache
		pk, err := NewPubKey(valid)
		if ref != nil {
			c.Check(err, NotNil)
			c.Check(pk, Equals, EmptyPubKey)
		} else {
			c.Check(err, IsNil)
			c.Check(pk.String(), Equals, valid)
		}
	}
	for i := 0; i < 2; i++ { // failures are never cached
		_, err := NewPubKey("tthorpub1invalid")
		c.Check(err, NotNil)
	}
}

func (s *PubKeyTestSuite) TestValidPubKeyCacheGenerations(c *C) {
	validPubKeyCacheMu.Lock()
	savedCur, savedPrev := validPubKeyCache, validPubKeyPrev
	validPubKeyCache, validPubKeyPrev = make(map[string]struct{}), make(map[string]struct{})
	validPubKeyCacheMu.Unlock()
	defer func() {
		validPubKeyCacheMu.Lock()
		validPubKeyCache, validPubKeyPrev = savedCur, savedPrev
		validPubKeyCacheMu.Unlock()
	}()

	_, pub, _ := testdata.KeyTestPubAddr()
	pk, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, pub)
	c.Assert(err, IsNil)
	_, err = NewPubKey(pk)
	c.Assert(err, IsNil)
	hot := cosmos.GetConfig().GetBech32AccountPubPrefix() + "|" + pk

	// fill past capacity with junk, touching the hot key once per generation
	for gen := 0; gen < 3; gen++ {
		for i := 0; i < validPubKeyCacheMax; i++ {
			rememberValidPubKey(fmt.Sprintf("junk-%d-%d", gen, i))
		}
		_, err = NewPubKey(pk)
		c.Assert(err, IsNil)
		c.Assert(len(validPubKeyCache) <= validPubKeyCacheMax, Equals, true)
		c.Assert(len(validPubKeyPrev) <= validPubKeyCacheMax, Equals, true)
	}
	_, inCur := validPubKeyCache[hot]
	_, inPrev := validPubKeyPrev[hot]
	c.Assert(inCur || inPrev, Equals, true)

	// failures are never cached, and still fail on a full cache
	_, err = NewPubKey("not-a-pubkey")
	c.Assert(err, NotNil)
	_, err = NewPubKey("not-a-pubkey")
	c.Assert(err, NotNil)
}

func (s *PubKeyTestSuite) TestGetThorAddressCache(c *C) {
	// the uncached computation
	reference := func(pk PubKey) (cosmos.AccAddress, error) {
		addr, err := pk.GetAddress(THORChain)
		if err != nil {
			return nil, err
		}
		return cosmos.AccAddressFromBech32(addr.String())
	}

	// THORChain account prefixes of the current network, restored afterwards
	config := cosmos.GetConfig()
	accPrefix, pubPrefix := config.GetBech32AccountAddrPrefix(), config.GetBech32AccountPubPrefix()
	defer config.SetBech32PrefixForAccount(accPrefix, pubPrefix)
	config.SetBech32PrefixForAccount("tthor", "tthorpub") // mocknet takes THORChain's prefix from the config
	thorPrefix := THORChain.AddressPrefix(CurrentChainNetwork)
	config.SetBech32PrefixForAccount(thorPrefix, thorPrefix+"pub")

	_, pub, _ := testdata.KeyTestPubAddr()
	bech32Pub, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, pub)
	c.Assert(err, IsNil)
	pk := PubKey(bech32Pub)
	want, err := reference(pk)
	c.Assert(err, IsNil)
	c.Assert(want.Empty(), Equals, false)
	for i := 0; i < 2; i++ { // second pass is served from the cache
		got, gerr := pk.GetThorAddress()
		c.Assert(gerr, IsNil)
		c.Check(got, DeepEquals, want)
		got[0] ^= 0xff // callers get their own copy
	}
	got, err := pk.GetThorAddress()
	c.Assert(err, IsNil)
	c.Check(got, DeepEquals, want)

	// the entry is keyed by the account address prefix: under another prefix
	// the decode fails exactly as it does uncached
	config.SetBech32PrefixForAccount("cosmos", thorPrefix+"pub")
	wantAddr, wantErr := reference(pk)
	c.Assert(wantErr, NotNil)
	gotAddr, gotErr := pk.GetThorAddress()
	c.Assert(gotErr, NotNil)
	c.Check(gotErr.Error(), Equals, wantErr.Error())
	c.Check(gotAddr, DeepEquals, wantAddr)
	config.SetBech32PrefixForAccount(thorPrefix, thorPrefix+"pub")

	// failures are never cached and match the uncached result exactly
	for _, bad := range []PubKey{EmptyPubKey, PubKey("tthorpub1invalid")} {
		wantAddr, wantErr := reference(bad)
		c.Assert(wantErr, NotNil)
		for i := 0; i < 2; i++ {
			gotAddr, gotErr := bad.GetThorAddress()
			c.Assert(gotErr, NotNil)
			c.Check(gotErr.Error(), Equals, wantErr.Error())
			c.Check(gotAddr, DeepEquals, wantAddr)
		}
	}
}
