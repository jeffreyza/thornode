package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"gitlab.com/thorchain/thornode/v3/common"
)

// GetValidateSeedPhraseCmd returns a side-effect-free command that validates a
// BIP39 mnemonic read from stdin, exiting non-zero if it is invalid. Any standard
// BIP39 length (12/15/18/21/24 words) is accepted, with checksum verification —
// except the grandfathered legacy shape: 24 wordlist words with an invalid
// checksum, accepted with a warning (see common.IsLegacyChecksumInvalidMnemonic).
// It is used by the entrypoint scripts to fail fast on a missing, empty, or corrupt
// signer seed phrase before any key material is provisioned. Note: a seed shorter
// than 24 words starts the node but cannot produce a keyshare DR backup (the signer
// warns loudly at keygen).
func GetValidateSeedPhraseCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "validate-seed-phrase",
		Short:             "Validate a BIP39 mnemonic read from stdin",
		Args:              cobra.ExactArgs(0),
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("fail to read mnemonic: %w", err)
			}
			phrase := strings.TrimSpace(string(data))
			if !common.IsValidMnemonic(phrase) {
				return fmt.Errorf("invalid seed phrase: must be a valid BIP39 mnemonic")
			}
			// A valid but shorter-than-24-word seed is accepted (the node runs and
			// signs) but cannot produce a keyshare disaster-recovery backup. Surface
			// that at provisioning — before the operator bonds — not only at the first
			// keygen. Still exit 0: the phrase is valid.
			if wordCount := len(strings.Fields(phrase)); wordCount < 24 {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"WARNING: signer seed is %d words: this node will run and sign but write NO keyshare disaster-recovery backup — regenerate as a 24-word seed to enable DR\n", wordCount)
			}
			// A 24-word wordlist-valid phrase with a bad checksum was accepted by the
			// pre-checksum validator, so bonded nodes may already run with one; it is
			// grandfathered (it derives the same keys it always did) with a warning.
			if common.IsLegacyChecksumInvalidMnemonic(phrase) {
				fmt.Fprintln(cmd.ErrOrStderr(),
					"WARNING: signer seed has an INVALID BIP39 checksum; accepted for legacy compatibility — consider migrating to a checksum-valid 24-word seed")
			}
			return nil
		},
	}
}
