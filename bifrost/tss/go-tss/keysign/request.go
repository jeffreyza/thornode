package keysign

import "gitlab.com/thorchain/thornode/v3/common"

// Request request to sign a message
type Request struct {
	PoolPubKey            string   `json:"pool_pub_key"`            // pub key of the pool that we would like to send this message from
	Messages              []string `json:"messages"`                // base64 encoded message to be signed
	SignerPubKeys         []string `json:"signer_pub_keys"`         // thornode-selected active signing party for this keysign
	NeverSigners          []string `json:"never_signers"`           // diagnostic signer pub keys that must not participate in keysign
	JailedFallbackSigners []string `json:"jailed_fallback_signers"` // diagnostic jailed signer pub keys that thornode may use as fallback
	RequiredSigners       int      `json:"required_signers"`        // diagnostic minimum number of active signers required for this keysign
	BlockHeight           int64    `json:"block_height"`            // THORChain block height associated with this keysign request
	Version               string   `json:"tss_version"`             // TSS protocol version to use for this keysign
	Algo                  string   `json:"algo"`                    // signing algorithm for this pool
}

// PolicyMetadata carries compatibility and diagnostic fields from thornode's
// keysign-party response.
type PolicyMetadata struct {
	NeverSigners          []string
	JailedFallbackSigners []string
	RequiredSigners       int
}

// NewRequest builds a keysign request for tests and callers that already have
// a resolved signer list. Policy metadata is optional because go-tss consumes
// SignerPubKeys as the authoritative active party.
func NewRequest(pk string, algo common.SigningAlgo, msgs []string, blockHeight int64, signers []string, version string, metadata ...PolicyMetadata) Request {
	req := Request{
		PoolPubKey:    pk,
		Algo:          string(algo),
		Messages:      msgs,
		SignerPubKeys: signers,
		BlockHeight:   blockHeight,
		Version:       version,
	}
	if len(metadata) > 0 {
		req.NeverSigners = metadata[0].NeverSigners
		req.JailedFallbackSigners = metadata[0].JailedFallbackSigners
		req.RequiredSigners = metadata[0].RequiredSigners
	} else {
		req.RequiredSigners = len(signers)
	}
	return req
}
