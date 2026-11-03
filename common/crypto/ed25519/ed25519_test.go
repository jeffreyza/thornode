package ed25519

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/mr-tron/base58"
	. "gopkg.in/check.v1"
)

func TestEd25519(t *testing.T) { TestingT(t) }

type Ed25519Suite struct{}

var _ = Suite(&Ed25519Suite{})

func (s *Ed25519Suite) TestAlgo(c *C) {
	mnemonic := "dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"

	// 64 bytes private key
	pk, err := Ed25519.Derive()(mnemonic, "", HDPath)
	c.Assert(err, IsNil)
	c.Assert(pk, NotNil)
	c.Assert(len(pk), Equals, 64)

	// 64 bytes cosmos.ed25519 private key
	privKey := Ed25519.Generate()(pk)
	b := privKey.Bytes()
	c.Assert(b, NotNil)
	c.Assert(len(b), Equals, 64)
	addr := base58.Encode(privKey.PubKey().Bytes())
	c.Assert(addr, Equals, "BJvLwohMSn9iL7x5oV7tc1eGH8D6qUiMd8ip6eVCtF73")
}

func (s *Ed25519Suite) TestAlgoName(c *C) {
	c.Assert(Ed25519.Name(), Equals, hd.Ed25519Type)
}

func (s *Ed25519Suite) TestSignerNameEDDSA(c *C) {
	c.Assert(SignerNameEDDSA("thorchain"), Equals, "ed-thorchain")
	c.Assert(SignerNameEDDSA(""), Equals, "ed-")
}

func (s *Ed25519Suite) TestParseDerivationPath(c *C) {
	// Valid hardened path
	path, err := ParseDerivationPath("m/44'/931'/0'/0'/0'")
	c.Assert(err, IsNil)
	c.Assert(len(path), Equals, 5)
	c.Assert(path[0], Equals, HardenedKeyStart+44)
	c.Assert(path[1], Equals, HardenedKeyStart+931)

	// Invalid prefix
	_, err = ParseDerivationPath("x/44'/0'")
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*should start with 'm/'.*")

	// Invalid hardened segment
	_, err = ParseDerivationPath("m/abc'")
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*invalid hardened segment.*")

	// Non-hardened segment (valid parse, no error)
	path, err = ParseDerivationPath("m/44")
	c.Assert(err, IsNil)
	c.Assert(path[0], Equals, uint32(44))

	// Invalid non-hardened segment
	_, err = ParseDerivationPath("m/abc")
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*invalid segment.*")

	// Hardened with 'h' suffix
	path, err = ParseDerivationPath("m/44h")
	c.Assert(err, IsNil)
	c.Assert(path[0], Equals, HardenedKeyStart+44)
}

func (s *Ed25519Suite) TestSeedFromMnemonic(c *C) {
	// Valid mnemonic
	mnemonic := "dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"
	seed, err := SeedFromMnemonic(mnemonic, "")
	c.Assert(err, IsNil)
	c.Assert(len(seed), Equals, 64)

	// Invalid mnemonic
	_, err = SeedFromMnemonic("invalid mnemonic words", "")
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*invalid mnemonic.*")
}

func (s *Ed25519Suite) TestMasterKeyFromSeed(c *C) {
	seed := make([]byte, 64)
	key, chainCode, err := MasterKeyFromSeed(seed)
	c.Assert(err, IsNil)
	c.Assert(len(key), Equals, 32)
	c.Assert(len(chainCode), Equals, 32)
}

func (s *Ed25519Suite) TestDerivePrivateKey(c *C) {
	seed := make([]byte, 64)
	masterKey, chainCode, err := MasterKeyFromSeed(seed)
	c.Assert(err, IsNil)

	// Valid hardened path
	path := DerivationPath{HardenedKeyStart + 44, HardenedKeyStart + 931}
	key, code, err := DerivePrivateKey(path, masterKey, chainCode)
	c.Assert(err, IsNil)
	c.Assert(len(key), Equals, 32)
	c.Assert(len(code), Equals, 32)

	// Non-hardened key should fail
	path = DerivationPath{44}
	_, _, err = DerivePrivateKey(path, masterKey, chainCode)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*non-hardened key derivation is not supported.*")
}

func (s *Ed25519Suite) TestDeriveKeypairFromMnemonic(c *C) {
	mnemonic := "dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"

	// Valid derivation
	keypair, err := DeriveKeypairFromMnemonic(mnemonic, "", HDPath)
	c.Assert(err, IsNil)
	c.Assert(len(keypair), Equals, 64)

	// Invalid path
	_, err = DeriveKeypairFromMnemonic(mnemonic, "", "bad/path")
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*invalid derivation path.*")

	// Invalid mnemonic
	_, err = DeriveKeypairFromMnemonic("bad mnemonic", "", HDPath)
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*invalid mnemonic.*")

	// Non-hardened path should fail at derive step
	_, err = DeriveKeypairFromMnemonic(mnemonic, "", "m/44")
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*error deriving private key.*")
}

func (s *Ed25519Suite) TestGetLegacyKeypairFromMnemonic(c *C) {
	mnemonic := "dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"

	// Valid legacy derivation
	keypair, err := GetLegacyKeypairFromMnemonic(mnemonic, "")
	c.Assert(err, IsNil)
	c.Assert(len(keypair), Equals, 64)

	// Invalid mnemonic
	_, err = GetLegacyKeypairFromMnemonic("bad mnemonic", "")
	c.Assert(err, NotNil)
	c.Assert(err, ErrorMatches, ".*invalid mnemonic.*")
}

func (s *Ed25519Suite) TestGetPrivateKeyFromMnemonic(c *C) {
	mnemonic := "dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"

	// Empty path uses legacy
	key, err := GetPrivateKeyFromMnemonic(mnemonic, "", "")
	c.Assert(err, IsNil)
	c.Assert(len(key), Equals, 64)

	// Non-empty path uses derivation
	key, err = GetPrivateKeyFromMnemonic(mnemonic, "", HDPath)
	c.Assert(err, IsNil)
	c.Assert(len(key), Equals, 64)

	// Empty path with invalid mnemonic
	_, err = GetPrivateKeyFromMnemonic("bad", "", "")
	c.Assert(err, NotNil)
}
