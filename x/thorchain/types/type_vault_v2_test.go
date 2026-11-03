package types

import (
	"crypto/ed25519"
	"encoding/hex"

	cmted25519 "github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/cosmos/cosmos-sdk/crypto/codec"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type VaultSuiteV2 struct{}

var _ = Suite(&VaultSuiteV2{})

func mustPubKeysFromSeed(c *C, seedHex string) (common.PubKey, common.PubKey) {
	privB, err := hex.DecodeString(seedHex)
	c.Assert(err, IsNil)

	secpPubAny, err := codec.FromCmtPubKeyInterface(secp256k1.PrivKey(privB).PubKey())
	c.Assert(err, IsNil)
	secpBech32, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, secpPubAny)
	c.Assert(err, IsNil)
	poolPk, err := common.NewPubKey(secpBech32)
	c.Assert(err, IsNil)

	eddsaPriv := ed25519.NewKeyFromSeed(privB[:32])
	eddsaPub, ok := eddsaPriv.Public().(ed25519.PublicKey)
	c.Assert(ok, Equals, true)
	eddsaPubAny, err := codec.FromCmtPubKeyInterface(cmted25519.PubKey(eddsaPub))
	c.Assert(err, IsNil)
	eddsaBech32, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, eddsaPubAny)
	c.Assert(err, IsNil)
	poolPkEddsa, err := common.NewPubKey(eddsaBech32)
	c.Assert(err, IsNil)

	return poolPk, poolPkEddsa
}

func (s *VaultSuiteV2) TestVaultV2(c *C) {
	SetupConfigForTest()

	vault := Vault{}
	c.Check(vault.IsEmpty(), Equals, true)
	c.Check(vault.Valid(), NotNil)

	poolPk, poolPkEddsa := mustPubKeysFromSeed(c, "ef235aacf90d9f4aadd8c92e4b2562e1d9eb97f0df9ba3b508258739cb013db2")
	vault = NewVaultV2(12, VaultStatus_ActiveVault, VaultType_AsgardVault, poolPk, common.Chains{common.ETHChain, common.SOLChain}.Strings(), []ChainContract{}, poolPkEddsa)

	// Test getting ECDSA Address
	ethAddr, err := vault.GetAddress(common.ETHChain)
	c.Check(err, IsNil)
	c.Check(ethAddr.String(), Equals, "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473")

	// Test Getting EdDSA Address
	solAddr, err := vault.GetAddress(common.SOLChain)
	c.Check(err, IsNil)
	c.Check(solAddr.String(), Equals, "DhMcGoLSWsKLqoM12GhR1ZxgcRFzZo2wZ8D2A3BvX9op")
}
