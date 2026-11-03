package common

import (
	"errors"
	"os"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/tyler-smith/go-bip39"
)

// GetSignerSeedPhrase returns the signer seed phrase, preferring the file
// path in SIGNER_SEED_PHRASE_FILE over the SIGNER_SEED_PHRASE env var.
//
// The file path is preferred so node-launcher can mount the seed phrase as a
// tmpfs volume instead of passing it through the container env, which causes
// containerd to persist the secret to the worker node's disk. The
// SIGNER_SEED_PHRASE env var is only honored on mocknet for local development;
// on all other networks the phrase must come from SIGNER_SEED_PHRASE_FILE.
func GetSignerSeedPhrase() (string, error) {
	if path := os.Getenv("SIGNER_SEED_PHRASE_FILE"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			log.Error().Err(err).Str("path", path).Msg("failed to read SIGNER_SEED_PHRASE_FILE")
		} else if phrase := strings.TrimSpace(string(data)); !IsValidMnemonic(phrase) {
			log.Error().Str("path", path).Msg("SIGNER_SEED_PHRASE_FILE content is not a valid BIP39 mnemonic")
		} else {
			if IsLegacyChecksumInvalidMnemonic(phrase) {
				log.Warn().Str("path", path).Msg("signer seed phrase has an INVALID BIP39 checksum; accepted for legacy compatibility (it derives the same keys it always did) — consider migrating to a checksum-valid 24-word seed")
			}
			return phrase, nil
		}
	}
	if CurrentChainNetwork == MockNet {
		if phrase := os.Getenv("SIGNER_SEED_PHRASE"); phrase != "" {
			return phrase, nil
		}
	}
	return "", errors.New("could not load signer seed phrase: SIGNER_SEED_PHRASE_FILE is unset or invalid (SIGNER_SEED_PHRASE env var is only honored on mocknet)")
}

// IsValidMnemonic reports whether phrase is acceptable as a signer seed: a valid
// BIP39 mnemonic of any standard length (12/15/18/21/24 words), or a legacy
// checksum-invalid 24-word phrase (see IsLegacyChecksumInvalidMnemonic). It uses
// tyler-smith/go-bip39, which validates the word count, wordlist membership, AND
// the checksum — so a phrase with a transposed or substituted (but still
// wordlist-valid) word is rejected. cosmos/go-bip39's IsMnemonicValid checks only
// count + wordlist (never the checksum), which is why it is not used here.
//
// Note: a seed shorter than 24 words is accepted so the node can start and sign, but
// it cannot produce a keyshare disaster-recovery backup (see encrypt_keyshares.go,
// which requires 24 words / 256-bit entropy); the signer warns loudly at keygen.
func IsValidMnemonic(phrase string) bool {
	// Require exactly one ASCII space between words. Validation libraries
	// tokenize with strings.Fields, while downstream consumers use the raw
	// phrase or split only on literal spaces.
	if phrase != strings.Join(strings.Fields(phrase), " ") {
		return false
	}
	return bip39.IsMnemonicValid(phrase) || IsLegacyChecksumInvalidMnemonic(phrase)
}

// IsLegacyChecksumInvalidMnemonic reports whether phrase is exactly the shape the
// pre-checksum validator accepted but strict validation rejects: 24 BIP39 wordlist
// words with an invalid checksum. Nodes may already be bonded with such a seed
// (cosmos/go-bip39 never verified the checksum), and BIP39 seed derivation is
// PBKDF2 over the raw words — the same keys derive regardless of checksum — so
// refusing the phrase post-upgrade would strand a working node. Callers accept it
// with a loud warning. Newly-accepted shorter lengths get no compat path.
func IsLegacyChecksumInvalidMnemonic(phrase string) bool {
	words := strings.Fields(phrase)
	if len(words) != 24 || bip39.IsMnemonicValid(phrase) {
		return false
	}
	for _, w := range words {
		if _, ok := bip39.GetWordIndex(w); !ok {
			return false
		}
	}
	return true
}
