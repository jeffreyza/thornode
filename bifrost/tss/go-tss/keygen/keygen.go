package keygen

import (
	bcrypto "gitlab.com/thorchain/tss/tss-lib-private.git/v3/crypto"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

type TssKeyGen interface {
	GenerateNewKey(keygenReq Request) (*bcrypto.ECPoint, error)
	GetTssKeyGenChannels() chan *p2p.Message
	GetTssCommonStruct() *common.TssCommon
}
