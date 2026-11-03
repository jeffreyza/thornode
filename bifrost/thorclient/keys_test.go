package thorclient

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

type KeysSuite struct{}

var _ = Suite(&KeysSuite{})

func (*KeysSuite) SetUpSuite(c *C) {
	thorchain.SetupConfigForTest()
}

const (
	signerNameForTest     = `jack`
	signerPasswordForTest = `password`
)

func (*KeysSuite) setupKeysForTest(c *C) string {
	ns := strconv.Itoa(time.Now().Nanosecond())
	thorcliDir := filepath.Join(os.TempDir(), ns, ".thorcli")
	c.Logf("thorcliDir:%s", thorcliDir)
	buf := bytes.NewBufferString(signerPasswordForTest)
	// the library used by keyring is using ReadLine , which expect a new line
	buf.WriteByte('\n')
	buf.WriteString(signerPasswordForTest)
	buf.WriteByte('\n')
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb, err := cKeys.New(cosmos.KeyringServiceName(), cKeys.BackendFile, thorcliDir, buf, cdc)
	c.Assert(err, IsNil)
	_, _, err = kb.NewMnemonic(signerNameForTest, cKeys.English, cmd.THORChainHDPath, signerPasswordForTest, hd.Secp256k1)
	c.Assert(err, IsNil)
	return thorcliDir
}

func (ks *KeysSuite) TestNewKeys(c *C) {
	oldStdIn := os.Stdin
	defer func() {
		os.Stdin = oldStdIn
	}()
	os.Stdin = nil
	folder := ks.setupKeysForTest(c)
	defer func() {
		err := os.RemoveAll(folder)
		c.Assert(err, IsNil)
	}()

	k, info, err := GetKeyringKeybase(folder, signerNameForTest, signerPasswordForTest)
	c.Assert(err, IsNil)
	c.Assert(k, NotNil)
	c.Assert(info, NotNil)
	ki := NewKeysWithKeybase(k, signerNameForTest, signerPasswordForTest)
	info = ki.GetSignerInfo()
	c.Assert(info, NotNil)
	c.Assert(info.Name, Equals, signerNameForTest)
	priKey, err := ki.GetPrivateKey()
	c.Assert(err, IsNil)
	c.Assert(priKey, NotNil)
	c.Assert(priKey.Bytes(), HasLen, 32)
	kb := ki.GetKeybase()
	c.Assert(kb, NotNil)
}

// armorCountingKeyring counts armored private key exports and hides the SDK
// keyring's ExportPrivateKeyObject.
type armorCountingKeyring struct {
	cKeys.Keyring
	armorExports int
}

func (k *armorCountingKeyring) ExportPrivKeyArmor(uid, encryptPassphrase string) (string, error) {
	k.armorExports++
	return k.Keyring.ExportPrivKeyArmor(uid, encryptPassphrase)
}

// directExportKeyring puts the SDK keyring's ExportPrivateKeyObject back.
type directExportKeyring struct {
	*armorCountingKeyring
}

func (k directExportKeyring) ExportPrivateKeyObject(uid string) (cryptotypes.PrivKey, error) {
	exporter, ok := k.Keyring.(privKeyExporter)
	if !ok {
		return nil, errors.New("keyring does not export private key objects")
	}
	return exporter.ExportPrivateKeyObject(uid)
}

func (*KeysSuite) TestGetPrivateKeyMatchesArmorRoundTrip(c *C) {
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb := cKeys.NewInMemory(cdc, func(options *cKeys.Options) {
		options.SupportedAlgos = append(options.SupportedAlgos, ed25519.Ed25519)
	})
	signerNameEDDSA := ed25519.SignerNameEDDSA(signerNameForTest)
	_, mnemonic, err := kb.NewMnemonic(signerNameForTest, cKeys.English, cmd.THORChainHDPath, signerPasswordForTest, hd.Secp256k1)
	c.Assert(err, IsNil)
	_, err = kb.NewAccount(signerNameEDDSA, mnemonic, signerPasswordForTest, "", ed25519.Ed25519)
	c.Assert(err, IsNil)

	// without the direct export every call pays two argon2 derivations
	_, ok := kb.(privKeyExporter)
	c.Assert(ok, Equals, true)

	unarmor := func(name string) []byte {
		armor, err := kb.ExportPrivKeyArmor(name, signerPasswordForTest)
		c.Assert(err, IsNil)
		priKey, _, err := crypto.UnarmorDecryptPrivKey(armor, signerPasswordForTest)
		c.Assert(err, IsNil)
		return priKey.Bytes()
	}
	secpWant := unarmor(signerNameForTest)
	eddsaWant := unarmor(signerNameEDDSA)
	c.Assert(secpWant, HasLen, 32)
	c.Assert(eddsaWant, HasLen, 64)

	counting := &armorCountingKeyring{Keyring: kb}
	direct := directExportKeyring{counting}
	missingErrs := make([]string, 0, 2)
	for _, tc := range []struct {
		kb           cKeys.Keyring
		armorExports int
	}{
		{kb: direct, armorExports: 0},
		{kb: counting, armorExports: 2},
	} {
		counting.armorExports = 0
		k := NewKeysWithKeybase(tc.kb, signerNameForTest, signerPasswordForTest)
		secpKey, err := k.GetPrivateKey()
		c.Assert(err, IsNil)
		c.Assert(secpKey.Bytes(), DeepEquals, secpWant)
		eddsaKey, err := k.GetPrivateKeyEDDSA()
		c.Assert(err, IsNil)
		c.Assert(eddsaKey.Bytes(), DeepEquals, eddsaWant)
		c.Assert(counting.armorExports, Equals, tc.armorExports)

		// each call hands out its own key
		secpKey.Key[0] ^= 0xff
		secpKey, err = k.GetPrivateKey()
		c.Assert(err, IsNil)
		c.Assert(secpKey.Bytes(), DeepEquals, secpWant)

		_, err = NewKeysWithKeybase(tc.kb, "nobody", signerPasswordForTest).GetPrivateKey()
		c.Assert(err, NotNil)
		missingErrs = append(missingErrs, err.Error())
	}
	c.Assert(missingErrs[0], Equals, missingErrs[1])
}
