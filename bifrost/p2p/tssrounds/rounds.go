// Package tssrounds holds the TSS round identifiers that Bifrost reports in keygen and
// keysign blame and that THORChain validates on-chain. It imports nothing, so the
// consensus packages (x/thorchain/types, and through it x/thorchain/memo) can use
// these constants without depending on bifrost/p2p/messages, which imports tss-lib.
package tssrounds

type Algo int

const (
	EDDSAKEYGEN1       = "EDDSAKGRound1Message"
	EDDSAKEYGEN2a      = "EDDSAKGRound2Message1"
	EDDSAKEYGEN2b      = "EDDSAKGRound2Message2"
	EDDSAKEYSIGN1      = "EDDSASignRound1Message"
	EDDSAKEYSIGN2      = "EDDSASignRound2Message"
	EDDSAKEYSIGN3      = "EDDSASignRound3Message"
	EDDSAKEYREGROUP1   = "EDDSADGRound1Message"
	EDDSAKEYREGROUP2   = "EDDSADGRound2Message"
	EDDSAKEYREGROUP3a  = "EDDSADGRound3Message1"
	EDDSAKEYREGROUP3b  = "EDDSADGRound3Message2"
	EDDSAKEYREGROUP4   = "EDDSADGRound4Message"
	EDDSAKEYGENROUNDS  = 3
	EDDSAKEYSIGNROUNDS = 3

	KEYGEN1          = "KGRound1Message"
	KEYGEN2aUnicast  = "KGRound2Message1"
	KEYGEN2b         = "KGRound2Message2"
	KEYGEN3          = "KGRound3Message"
	KEYSIGN1aUnicast = "SignRound1Message1"
	KEYSIGN1b        = "SignRound1Message2"
	KEYSIGN2Unicast  = "SignRound2Message"
	KEYSIGN3         = "SignRound3Message"
	KEYSIGN4         = "SignRound4Message"
	KEYSIGN5         = "SignRound5Message"
	KEYSIGN6         = "SignRound6Message"
	KEYSIGN7         = "SignRound7Message"
	KEYSIGN8         = "SignRound8Message"
	KEYSIGN9         = "SignRound9Message"
	// FROST keysign failures use these ordinary ceremony rounds. They remain
	// distinct from the FROSTV2 equivocation-evidence identifiers, which are
	// not accepted on-chain until their evidence is validated by consensus.
	FROSTKEYSIGNPREPROCESS = "frost_preprocess"
	FROSTKEYSIGNSHARE      = "frost_share"
	FROSTKEYSIGNRESULT     = "frost_result"
	TSSKEYGENROUNDS        = 4
	// TSSKEYSIGNROUNDS is sized to fit every ECDSA keysign message index
	// (round 1 splits into Message1/Message2, then rounds 2..9 add one each),
	// i.e. max blame.RoundInfo.Index + 1 = 10. tss-lib v3 added rounds 8 and 9.
	TSSKEYSIGNROUNDS = 10

	ECDSAKEYGEN Algo = iota
	ECDSAKEYSIGN
	EDDSAKEYGEN
	EDDSAKEYSIGN
)

// ValidTssKeysignRounds is the accepted canonical keysign-round set after
// alias normalization. Defined here alongside the constants so additions to
// the round constants are reflected in the validity set.
var ValidTssKeysignRounds = map[string]bool{
	KEYSIGN1aUnicast:       true,
	KEYSIGN1b:              true,
	KEYSIGN2Unicast:        true,
	KEYSIGN3:               true,
	KEYSIGN4:               true,
	KEYSIGN5:               true,
	KEYSIGN6:               true,
	KEYSIGN7:               true,
	KEYSIGN8:               true,
	KEYSIGN9:               true,
	EDDSAKEYSIGN1:          true,
	EDDSAKEYSIGN2:          true,
	EDDSAKEYSIGN3:          true,
	FROSTKEYSIGNPREPROCESS: true,
	FROSTKEYSIGNSHARE:      true,
	FROSTKEYSIGNRESULT:     true,
}
