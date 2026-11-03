package messages

import "gitlab.com/thorchain/thornode/v3/bifrost/p2p/tssrounds"

// The TSS round identifiers live in bifrost/p2p/tssrounds, which consensus code
// imports without pulling in tss-lib. These aliases keep the Bifrost call sites unchanged.

type Algo = tssrounds.Algo

const (
	EDDSAKEYGEN1           = tssrounds.EDDSAKEYGEN1
	EDDSAKEYGEN2a          = tssrounds.EDDSAKEYGEN2a
	EDDSAKEYGEN2b          = tssrounds.EDDSAKEYGEN2b
	EDDSAKEYSIGN1          = tssrounds.EDDSAKEYSIGN1
	EDDSAKEYSIGN2          = tssrounds.EDDSAKEYSIGN2
	EDDSAKEYSIGN3          = tssrounds.EDDSAKEYSIGN3
	EDDSAKEYREGROUP1       = tssrounds.EDDSAKEYREGROUP1
	EDDSAKEYREGROUP2       = tssrounds.EDDSAKEYREGROUP2
	EDDSAKEYREGROUP3a      = tssrounds.EDDSAKEYREGROUP3a
	EDDSAKEYREGROUP3b      = tssrounds.EDDSAKEYREGROUP3b
	EDDSAKEYREGROUP4       = tssrounds.EDDSAKEYREGROUP4
	EDDSAKEYGENROUNDS      = tssrounds.EDDSAKEYGENROUNDS
	EDDSAKEYSIGNROUNDS     = tssrounds.EDDSAKEYSIGNROUNDS
	KEYGEN1                = tssrounds.KEYGEN1
	KEYGEN2aUnicast        = tssrounds.KEYGEN2aUnicast
	KEYGEN2b               = tssrounds.KEYGEN2b
	KEYGEN3                = tssrounds.KEYGEN3
	KEYSIGN1aUnicast       = tssrounds.KEYSIGN1aUnicast
	KEYSIGN1b              = tssrounds.KEYSIGN1b
	KEYSIGN2Unicast        = tssrounds.KEYSIGN2Unicast
	KEYSIGN3               = tssrounds.KEYSIGN3
	KEYSIGN4               = tssrounds.KEYSIGN4
	KEYSIGN5               = tssrounds.KEYSIGN5
	KEYSIGN6               = tssrounds.KEYSIGN6
	KEYSIGN7               = tssrounds.KEYSIGN7
	KEYSIGN8               = tssrounds.KEYSIGN8
	KEYSIGN9               = tssrounds.KEYSIGN9
	FROSTKEYSIGNPREPROCESS = tssrounds.FROSTKEYSIGNPREPROCESS
	FROSTKEYSIGNSHARE      = tssrounds.FROSTKEYSIGNSHARE
	FROSTKEYSIGNRESULT     = tssrounds.FROSTKEYSIGNRESULT
	TSSKEYGENROUNDS        = tssrounds.TSSKEYGENROUNDS
	TSSKEYSIGNROUNDS       = tssrounds.TSSKEYSIGNROUNDS
	ECDSAKEYGEN            = tssrounds.ECDSAKEYGEN
	ECDSAKEYSIGN           = tssrounds.ECDSAKEYSIGN
	EDDSAKEYGEN            = tssrounds.EDDSAKEYGEN
	EDDSAKEYSIGN           = tssrounds.EDDSAKEYSIGN
)

// ValidTssKeysignRounds is tssrounds.ValidTssKeysignRounds (the same map).
var ValidTssKeysignRounds = tssrounds.ValidTssKeysignRounds
