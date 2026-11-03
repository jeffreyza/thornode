package common

import (
	"os"
	"path/filepath"
	"strings"

	. "gopkg.in/check.v1"
)

type SeedPhraseSuite struct{}

var _ = Suite(&SeedPhraseSuite{})

const testMnemonic = "dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"

func (s *SeedPhraseSuite) SetUpTest(c *C) {
	c.Assert(os.Unsetenv("SIGNER_SEED_PHRASE"), IsNil)
	c.Assert(os.Unsetenv("SIGNER_SEED_PHRASE_FILE"), IsNil)
}

func (s *SeedPhraseSuite) TearDownTest(c *C) {
	c.Assert(os.Unsetenv("SIGNER_SEED_PHRASE"), IsNil)
	c.Assert(os.Unsetenv("SIGNER_SEED_PHRASE_FILE"), IsNil)
}

// envVarHonored reports whether the SIGNER_SEED_PHRASE env-var fallback is
// active for the build's network. It is only honored on mocknet.
func envVarHonored() bool { return CurrentChainNetwork == MockNet }

func (s SeedPhraseSuite) TestIsValidMnemonic_LengthsAndChecksum(c *C) {
	// 12-word all-zero-entropy BIP39 vector (valid checksum) — accepted under the
	// grace path (node can start; DR backup unavailable, warned at keygen).
	c.Check(IsValidMnemonic("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"), Equals, true)
	// 24-word all-zero-entropy vector (valid checksum).
	c.Check(IsValidMnemonic("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art"), Equals, true)
	// Valid words + valid length but WRONG checksum → rejected. This is exactly what
	// the switch to a checksum-validating check buys: cosmos/go-bip39 accepted this.
	c.Check(IsValidMnemonic("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon"), Equals, false)
	// Non-BIP39 word count / garbage.
	c.Check(IsValidMnemonic("dog dog dog"), Equals, false)
	c.Check(IsValidMnemonic(""), Equals, false)
}

func (s SeedPhraseSuite) TestLegacyChecksumInvalid24WordCompat(c *C) {
	// 24 wordlist words with an INVALID checksum: the pre-checksum validator
	// (cosmos/go-bip39) accepted this shape, so nodes may already be bonded with
	// such a seed on mainnet — it must stay accepted (with a warning at the call
	// sites) or the upgrade strands a working node. BIP39 seed derivation is
	// PBKDF2 over the raw words, so the same keys derive regardless of checksum.
	legacy := strings.TrimSpace(strings.Repeat("abandon ", 24))
	c.Check(IsLegacyChecksumInvalidMnemonic(legacy), Equals, true)
	c.Check(IsValidMnemonic(legacy), Equals, true)

	// A checksum-VALID 24-word phrase is not "legacy" — no warning path.
	valid24 := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art"
	c.Check(IsLegacyChecksumInvalidMnemonic(valid24), Equals, false)

	// The compat path is exactly the 24-word shape the old validator accepted:
	// shorter checksum-invalid phrases are newly accepted lengths and stay strict…
	c.Check(IsLegacyChecksumInvalidMnemonic("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon"), Equals, false)
	// …and a non-wordlist word was never accepted, so it stays rejected.
	notAWord := strings.TrimSpace(strings.Repeat("abandon ", 23) + "blorp")
	c.Check(IsLegacyChecksumInvalidMnemonic(notAWord), Equals, false)
	c.Check(IsValidMnemonic(notAWord), Equals, false)
}

func (s SeedPhraseSuite) TestGetSignerSeedPhrase_EnvVarOnly(c *C) {
	c.Assert(os.Setenv("SIGNER_SEED_PHRASE", testMnemonic), IsNil)

	got, err := GetSignerSeedPhrase()
	if envVarHonored() {
		c.Assert(err, IsNil)
		c.Check(got, Equals, testMnemonic)
	} else {
		c.Assert(err, NotNil)
		c.Check(got, Equals, "")
	}
}

func (s SeedPhraseSuite) TestGetSignerSeedPhrase_FileOnly(c *C) {
	path := filepath.Join(c.MkDir(), "seed-phrase")
	c.Assert(os.WriteFile(path, []byte("  "+testMnemonic+"\n"), 0o600), IsNil)
	c.Assert(os.Setenv("SIGNER_SEED_PHRASE_FILE", path), IsNil)

	got, err := GetSignerSeedPhrase()
	c.Assert(err, IsNil)
	c.Check(got, Equals, testMnemonic)
}

func (s SeedPhraseSuite) TestGetSignerSeedPhrase_FileTakesPriority(c *C) {
	fileMnemonic := "cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat crawl"
	path := filepath.Join(c.MkDir(), "seed-phrase")
	c.Assert(os.WriteFile(path, []byte(fileMnemonic+"\n"), 0o600), IsNil)
	c.Assert(os.Setenv("SIGNER_SEED_PHRASE_FILE", path), IsNil)
	c.Assert(os.Setenv("SIGNER_SEED_PHRASE", testMnemonic), IsNil)

	got, err := GetSignerSeedPhrase()
	c.Assert(err, IsNil)
	c.Check(got, Equals, fileMnemonic)
}

func (s SeedPhraseSuite) TestGetSignerSeedPhrase_BothMissingReturnsError(c *C) {
	got, err := GetSignerSeedPhrase()
	c.Check(got, Equals, "")
	c.Assert(err, NotNil)
}

func (s SeedPhraseSuite) TestGetSignerSeedPhrase_FileMissingFallsBackToEnvVar(c *C) {
	c.Assert(os.Setenv("SIGNER_SEED_PHRASE_FILE", "/nonexistent/seed-phrase-file"), IsNil)
	c.Assert(os.Setenv("SIGNER_SEED_PHRASE", testMnemonic), IsNil)

	got, err := GetSignerSeedPhrase()
	if envVarHonored() {
		c.Assert(err, IsNil)
		c.Check(got, Equals, testMnemonic)
	} else {
		c.Assert(err, NotNil)
		c.Check(got, Equals, "")
	}
}

func (s SeedPhraseSuite) TestGetSignerSeedPhrase_FileMissingNoEnvVarReturnsError(c *C) {
	c.Assert(os.Setenv("SIGNER_SEED_PHRASE_FILE", "/nonexistent/seed-phrase-file"), IsNil)

	_, err := GetSignerSeedPhrase()
	c.Assert(err, NotNil)
}

func (s SeedPhraseSuite) TestGetSignerSeedPhrase_FileCorruptFallsBackToEnvVar(c *C) {
	path := filepath.Join(c.MkDir(), "seed-phrase")
	c.Assert(os.WriteFile(path, []byte("not a valid mnemonic"), 0o600), IsNil)
	c.Assert(os.Setenv("SIGNER_SEED_PHRASE_FILE", path), IsNil)
	c.Assert(os.Setenv("SIGNER_SEED_PHRASE", testMnemonic), IsNil)

	got, err := GetSignerSeedPhrase()
	if envVarHonored() {
		c.Assert(err, IsNil)
		c.Check(got, Equals, testMnemonic)
	} else {
		c.Assert(err, NotNil)
		c.Check(got, Equals, "")
	}
}

func (s SeedPhraseSuite) TestGetSignerSeedPhrase_FileEmptyFallsBackToEnvVar(c *C) {
	path := filepath.Join(c.MkDir(), "seed-phrase")
	c.Assert(os.WriteFile(path, []byte("   \n"), 0o600), IsNil)
	c.Assert(os.Setenv("SIGNER_SEED_PHRASE_FILE", path), IsNil)
	c.Assert(os.Setenv("SIGNER_SEED_PHRASE", testMnemonic), IsNil)

	got, err := GetSignerSeedPhrase()
	if envVarHonored() {
		c.Assert(err, IsNil)
		c.Check(got, Equals, testMnemonic)
	} else {
		c.Assert(err, NotNil)
		c.Check(got, Equals, "")
	}
}
