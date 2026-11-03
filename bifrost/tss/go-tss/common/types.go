package common

import (
	"time"
)

type TssConfig struct {
	// Party Timeout defines how long do we wait for the party to form
	PartyTimeout time.Duration
	// KeyGenTimeoutSeconds defines how long do we wait the keygen parties to pass messages along
	KeyGenTimeout time.Duration
	// FrostKeygenTimeout defines how long do we wait the FROST DKG to complete
	// each round. FROST is a 3-round p2p protocol and needs its own timeout,
	// independent of KeyGenTimeout which controls ECDSA/EDDSA keygen timing.
	FrostKeygenTimeout time.Duration
	// KeySignTimeoutSeconds defines how long do we wait keysign
	KeySignTimeout time.Duration
	// Pre-parameter define the pre-parameter generations timeout
	PreParamTimeout time.Duration
	// enable the tss monitor
	EnableMonitor bool
}
