package keysign

import (
	bc "gitlab.com/thorchain/tss/tss-lib-private.git/v3/common"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

type TssKeySign interface {
	GetTssKeySignChannels() chan *p2p.Message
	GetTssCommonStruct() *common.TssCommon
	// version is the consensus-agreed THORChain network version from the
	// keysign request; it gates session-nonce activation.
	SignMessage(msgToSign [][]byte, localStateItem storage.KeygenLocalState, parties []string, version string) ([]*bc.SignatureData, error)
}
