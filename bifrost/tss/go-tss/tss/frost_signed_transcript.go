package tss

import (
	"fmt"
	"strings"

	tcrypto "github.com/cometbft/cometbft/crypto"
	legacybech32 "github.com/cosmos/cosmos-sdk/types/bech32/legacybech32"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

const frostMaxNodeSignatureBytes = 128

func newFrostSignedEnvelope(
	privateKey tcrypto.PrivKey,
	sessionID, vaultPubKey, network, round, senderPubKey string,
	participantIndex uint32,
	payloadHash string,
) (messages.FrostSignedEnvelope, error) {
	if privateKey == nil {
		return messages.FrostSignedEnvelope{}, fmt.Errorf("FROST transcript signing key is unavailable")
	}
	envelope := messages.FrostSignedEnvelope{
		ProtocolVersion:  messages.FrostProtocolVersion,
		SessionID:        strings.TrimSpace(sessionID),
		VaultPubKey:      strings.TrimSpace(vaultPubKey),
		Network:          strings.TrimSpace(network),
		Round:            strings.TrimSpace(round),
		SenderPubKey:     strings.TrimSpace(senderPubKey),
		ParticipantIndex: participantIndex,
		PayloadHash:      strings.ToLower(strings.TrimSpace(payloadHash)),
	}
	signBytes, err := envelope.SignBytes()
	if err != nil {
		return messages.FrostSignedEnvelope{}, err
	}
	envelope.Signature, err = privateKey.Sign(signBytes)
	if err != nil {
		return messages.FrostSignedEnvelope{}, fmt.Errorf("failed to sign FROST transcript envelope: %w", err)
	}
	return envelope, nil
}

func verifyFrostSignedEnvelope(envelope messages.FrostSignedEnvelope) error {
	signBytes, err := envelope.SignBytes()
	if err != nil {
		return err
	}
	if len(envelope.Signature) == 0 || len(envelope.Signature) > frostMaxNodeSignatureBytes {
		return fmt.Errorf("FROST transcript envelope signature is missing")
	}
	pubKey, err := legacybech32.UnmarshalPubKey(legacybech32.AccPK, envelope.SenderPubKey) //nolint:staticcheck
	if err != nil {
		return fmt.Errorf("invalid FROST transcript signer pubkey")
	}
	if !pubKey.VerifySignature(signBytes, envelope.Signature) {
		return fmt.Errorf("invalid FROST transcript envelope signature")
	}
	return nil
}

func newFrostSignedEcho(
	privateKey tcrypto.PrivKey,
	attesterPubKey string,
	target messages.FrostSignedEnvelope,
) (messages.FrostSignedEcho, error) {
	if privateKey == nil {
		return messages.FrostSignedEcho{}, fmt.Errorf("FROST transcript signing key is unavailable")
	}
	echo := messages.FrostSignedEcho{
		ProtocolVersion: messages.FrostProtocolVersion,
		AttesterPubKey:  strings.TrimSpace(attesterPubKey),
		Target:          target,
	}
	signBytes, err := echo.SignBytes()
	if err != nil {
		return messages.FrostSignedEcho{}, err
	}
	echo.Signature, err = privateKey.Sign(signBytes)
	if err != nil {
		return messages.FrostSignedEcho{}, fmt.Errorf("failed to sign FROST transcript echo: %w", err)
	}
	return echo, nil
}

func verifyFrostSignedEcho(echo messages.FrostSignedEcho) error {
	if err := verifyFrostSignedEnvelope(echo.Target); err != nil {
		return err
	}
	signBytes, err := echo.SignBytes()
	if err != nil {
		return err
	}
	if len(echo.Signature) == 0 || len(echo.Signature) > frostMaxNodeSignatureBytes {
		return fmt.Errorf("FROST transcript echo signature is missing")
	}
	pubKey, err := legacybech32.UnmarshalPubKey(legacybech32.AccPK, echo.AttesterPubKey) //nolint:staticcheck
	if err != nil {
		return fmt.Errorf("invalid FROST transcript echo pubkey")
	}
	if !pubKey.VerifySignature(signBytes, echo.Signature) {
		return fmt.Errorf("invalid FROST transcript echo signature")
	}
	return nil
}

func frostEnvelopeFromPreprocess(msgID, vaultPubKey, network string, payload FrostPreprocessPayload, payloadHash string) messages.FrostSignedEnvelope {
	return messages.FrostSignedEnvelope{
		ProtocolVersion:  payload.ProtocolVersion,
		SessionID:        msgID,
		VaultPubKey:      vaultPubKey,
		Network:          network,
		Round:            frostRoundPreprocess,
		SenderPubKey:     payload.SenderPubKey,
		ParticipantIndex: payload.ParticipantIndex,
		PayloadHash:      payloadHash,
		Signature:        payload.Signature,
	}
}

func frostEnvelopeFromShare(msgID, vaultPubKey, network string, payload FrostSharePayload, payloadHash string) messages.FrostSignedEnvelope {
	return messages.FrostSignedEnvelope{
		ProtocolVersion:  payload.ProtocolVersion,
		SessionID:        msgID,
		VaultPubKey:      vaultPubKey,
		Network:          network,
		Round:            frostRoundShare,
		SenderPubKey:     payload.SenderPubKey,
		ParticipantIndex: payload.ParticipantIndex,
		PayloadHash:      payloadHash,
		Signature:        payload.Signature,
	}
}

func frostEchoFromVerify(msgID, vaultPubKey, network string, payload FrostVerifyPayload) messages.FrostSignedEcho {
	return messages.FrostSignedEcho{
		ProtocolVersion: payload.ProtocolVersion,
		AttesterPubKey:  payload.SenderPubKey,
		Target: messages.FrostSignedEnvelope{
			ProtocolVersion:  payload.ProtocolVersion,
			SessionID:        msgID,
			VaultPubKey:      vaultPubKey,
			Network:          network,
			Round:            payload.Round,
			SenderPubKey:     payload.TargetSenderPubKey,
			ParticipantIndex: payload.TargetParticipantIndex,
			PayloadHash:      payload.PayloadHash,
			Signature:        payload.TargetSignature,
		},
		Signature: payload.AttesterSignature,
	}
}
