package types

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// NewMsgTssPool is a constructor function for MsgTssPool
func NewMsgTssPoolV2(
	pks []string,
	poolpk common.PubKey,
	secp256k1Signature,
	keysharesBackup []byte,
	keygenType KeygenType,
	height int64,
	bl []Blame,
	chains []string,
	signer cosmos.AccAddress,
	keygenTime int64,
	poolPubKeyEddsa common.PubKey,
	keysharesBackupEddsa []byte,
	keysharesBackupFrost []byte,
	poolPubKeyFrost common.PubKey,
) (*MsgTssPool, error) {
	id, err := getTssID(pks, poolpk, poolPubKeyEddsa, height, bl)
	if keygenType == KeygenType_XMRMonovaultKeygen {
		id, err = getXMRTssID(pks, poolpk, poolPubKeyEddsa, height, bl, poolPubKeyFrost, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("fail to get tss id: %w", err)
	}
	return &MsgTssPool{
		ID:                   id,
		PubKeys:              pks,
		PoolPubKey:           poolpk,
		PoolPubKeyEddsa:      poolPubKeyEddsa,
		PoolPubKeyFrost:      poolPubKeyFrost,
		Height:               height,
		KeygenType:           keygenType,
		Blame:                bl,
		Chains:               chains,
		Signer:               signer,
		KeygenTime:           keygenTime,
		KeysharesBackup:      keysharesBackup,
		KeysharesBackupEddsa: keysharesBackupEddsa,
		KeysharesBackupFrost: keysharesBackupFrost,
		Secp256K1Signature:   secp256k1Signature,
	}, nil
}

func NewMsgTssPoolV3(
	pks []string,
	poolpk common.PubKey,
	secp256k1Signature,
	keysharesBackup []byte,
	keygenType KeygenType,
	height int64,
	bl []Blame,
	chains []string,
	signer cosmos.AccAddress,
	keygenTime int64,
	poolPubKeyEddsa common.PubKey,
	keysharesBackupEddsa []byte,
	keysharesBackupFrost []byte,
	poolPubKeyFrost common.PubKey,
	observedMoneroHeight int64,
) (*MsgTssPool, error) {
	msg, err := NewMsgTssPoolV2(pks, poolpk, secp256k1Signature, keysharesBackup, keygenType, height, bl, chains, signer, keygenTime, poolPubKeyEddsa, keysharesBackupEddsa, keysharesBackupFrost, poolPubKeyFrost)
	if err != nil {
		return nil, err
	}
	msg.ObservedMoneroHeight = observedMoneroHeight
	if keygenType == KeygenType_XMRMonovaultKeygen {
		id, idErr := getXMRTssID(pks, poolpk, poolPubKeyEddsa, height, bl, poolPubKeyFrost, observedMoneroHeight)
		if idErr != nil {
			return nil, fmt.Errorf("fail to get tss id: %w", idErr)
		}
		msg.ID = id
	}
	return msg, nil
}
