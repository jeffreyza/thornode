package sidecar

import (
	"context"
	"fmt"

	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
)

// FrostAdapter adapts a sidecar.Client to the go-tss FrostSidecarClient
// interface. Method names on this type mirror the tssp interface exactly
// (that's the contract); internally they delegate to the shorter, more
// conventional Client methods.
type FrostAdapter struct {
	client *Client
	ctx    context.Context
}

// NewFrostAdapter wires a sidecar.Client into the go-tss FROST coordinator.
func NewFrostAdapter(client *Client) *FrostAdapter {
	return NewFrostAdapterWithContext(client, context.Background())
}

// NewFrostAdapterWithContext wires lifecycle cancellation into sidecar calls.
func NewFrostAdapterWithContext(client *Client, ctx context.Context) *FrostAdapter {
	if ctx == nil {
		ctx = context.Background()
	}
	return &FrostAdapter{client: client, ctx: ctx}
}

// NewFrostAdapterWithStop adapts the chain client's lifecycle channel without
// allocating a watcher goroutine for every signing attempt.
func NewFrostAdapterWithStop(client *Client, stop <-chan struct{}) *FrostAdapter {
	if stop == nil {
		return NewFrostAdapter(client)
	}
	return NewFrostAdapterWithContext(client, stopChannelContext{
		Context: context.Background(),
		done:    stop,
	})
}

type stopChannelContext struct {
	context.Context
	done <-chan struct{}
}

func (c stopChannelContext) Done() <-chan struct{} { return c.done }

func (c stopChannelContext) Err() error {
	select {
	case <-c.done:
		return context.Canceled
	default:
		return nil
	}
}

func (a *FrostAdapter) FrostSignPrepare(req tssp.FrostSidecarPrepareReq) (tssp.FrostSidecarPrepareResp, error) {
	if a == nil || a.client == nil {
		return tssp.FrostSidecarPrepareResp{}, fmt.Errorf("frost adapter is not initialized")
	}
	resp, err := a.client.SignPrepare(SignPrepareReq{
		MsgID:                       req.MsgID,
		VaultPubKey:                 req.VaultPubKey,
		ToAddress:                   req.ToAddress,
		AmountAtomic:                req.AmountAtomic,
		Memo:                        req.Memo,
		FeeRate:                     req.FeeRate,
		DecoyContext:                req.DecoyContext,
		RequiredInputs:              spentRefsFromTSS(req.RequiredInputs),
		MaxInputs:                   req.MaxInputs,
		DustThreshold:               req.DustThreshold,
		MaxSelectedInputValueAtomic: req.MaxSelectedInputValueAtomic,
		MaxGasAtomic:                req.MaxGasAtomic,
	})
	if err != nil {
		return tssp.FrostSidecarPrepareResp{}, err
	}
	return tssp.FrostSidecarPrepareResp{
		SessionID:      resp.SessionID,
		Preprocess:     resp.Preprocess,
		SelectedInputs: spentRefsToTSS(resp.SelectedInputs),
	}, nil
}

func (a *FrostAdapter) FrostSignPartial(req tssp.FrostSidecarPartialReq) (tssp.FrostSidecarPartialResp, error) {
	if a == nil || a.client == nil {
		return tssp.FrostSidecarPartialResp{}, fmt.Errorf("frost adapter is not initialized")
	}
	resp, err := a.client.SignPartialContext(a.ctx, SignPartialReq{
		MsgID:           req.MsgID,
		SessionID:       req.SessionID,
		AllPreprocesses: req.AllPreprocesses,
	})
	if err != nil {
		return tssp.FrostSidecarPartialResp{}, err
	}
	return tssp.FrostSidecarPartialResp{
		SignatureShare: resp.SignatureShare,
	}, nil
}

func (a *FrostAdapter) FrostSignCombine(req tssp.FrostSidecarCombineReq) (tssp.FrostSidecarCombineResp, error) {
	if a == nil || a.client == nil {
		return tssp.FrostSidecarCombineResp{}, fmt.Errorf("frost adapter is not initialized")
	}
	resp, err := a.client.SignCombineContext(a.ctx, SignCombineReq{
		MsgID:     req.MsgID,
		SessionID: req.SessionID,
		AllShares: req.AllShares,
	})
	if err != nil {
		return tssp.FrostSidecarCombineResp{}, err
	}
	var spentRefs []tssp.FrostSpentOutputRef
	for _, ref := range resp.SpentOutputRefs {
		spentRefs = append(spentRefs, tssp.FrostSpentOutputRef{
			TxHash:      ref.TxHash,
			OutputIndex: ref.OutputIndex,
			KeyImage:    ref.KeyImage,
			SpendTxHash: ref.SpendTxHash,
		})
	}
	return tssp.FrostSidecarCombineResp{
		TxHash:          resp.TxHash,
		TxHex:           resp.TxHex,
		TxKey:           resp.TxKey,
		FeeAtomic:       resp.FeeAtomic,
		SpentOutputRefs: spentRefs,
	}, nil
}

func (a *FrostAdapter) FrostDKGRound1(req tssp.FrostSidecarDKGRound1Req) (tssp.FrostSidecarDKGRound1Resp, error) {
	if a == nil || a.client == nil {
		return tssp.FrostSidecarDKGRound1Resp{}, fmt.Errorf("frost adapter is not initialized")
	}
	resp, err := a.client.DKGRound1(DKGRound1Req{
		VaultPubKey:      req.VaultPubKey,
		Threshold:        req.Threshold,
		N:                req.N,
		ParticipantIndex: req.ParticipantIndex,
		Context:          req.Context,
		SessionID:        req.SessionID,
	})
	if err != nil {
		return tssp.FrostSidecarDKGRound1Resp{}, err
	}
	return tssp.FrostSidecarDKGRound1Resp{Commitments: resp.Commitments}, nil
}

func (a *FrostAdapter) FrostDKGRound2(req tssp.FrostSidecarDKGRound2Req) (tssp.FrostSidecarDKGRound2Resp, error) {
	if a == nil || a.client == nil {
		return tssp.FrostSidecarDKGRound2Resp{}, fmt.Errorf("frost adapter is not initialized")
	}
	resp, err := a.client.DKGRound2(DKGRound2Req{
		SessionID:      req.SessionID,
		AllCommitments: req.AllCommitments,
	})
	if err != nil {
		return tssp.FrostSidecarDKGRound2Resp{}, err
	}
	return tssp.FrostSidecarDKGRound2Resp{EncryptedShares: resp.EncryptedShares}, nil
}

func (a *FrostAdapter) FrostDKGRound3(req tssp.FrostSidecarDKGRound3Req) (tssp.FrostSidecarDKGRound3Resp, error) {
	if a == nil || a.client == nil {
		return tssp.FrostSidecarDKGRound3Resp{}, fmt.Errorf("frost adapter is not initialized")
	}
	resp, err := a.client.DKGRound3(DKGRound3Req{
		SessionID: req.SessionID,
		AllShares: req.AllShares,
	})
	if err != nil {
		return tssp.FrostSidecarDKGRound3Resp{}, err
	}
	return tssp.FrostSidecarDKGRound3Resp{
		Address:  resp.Address,
		GroupKey: resp.GroupKey,
	}, nil
}

func (a *FrostAdapter) FrostDKGFinalize(req tssp.FrostSidecarDKGFinalizeReq) (tssp.FrostSidecarDKGFinalizeResp, error) {
	if a == nil || a.client == nil {
		return tssp.FrostSidecarDKGFinalizeResp{}, fmt.Errorf("frost adapter is not initialized")
	}
	resp, err := a.client.DKGFinalize(DKGFinalizeReq{
		SessionID: req.SessionID,
		GroupKey:  req.GroupKey,
		Address:   req.Address,
	})
	if err != nil {
		return tssp.FrostSidecarDKGFinalizeResp{}, err
	}
	return tssp.FrostSidecarDKGFinalizeResp{Success: resp.Success, KeyBytesHex: resp.KeyBytesHex}, nil
}

// GetChainHeight is part of the go-tss sidecar interface — keep the name.
func (a *FrostAdapter) GetChainHeight() (int64, error) {
	if a == nil || a.client == nil {
		return 0, fmt.Errorf("frost adapter is not initialized")
	}
	return a.client.ChainHeight()
}

func spentRefsFromTSS(refs []tssp.FrostSpentOutputRef) []SpentRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]SpentRef, 0, len(refs))
	for _, ref := range refs {
		out = append(out, SpentRef{
			TxHash:      ref.TxHash,
			OutputIndex: ref.OutputIndex,
			KeyImage:    ref.KeyImage,
			SpendTxHash: ref.SpendTxHash,
		})
	}
	return out
}

func spentRefsToTSS(refs []SpentRef) []tssp.FrostSpentOutputRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]tssp.FrostSpentOutputRef, 0, len(refs))
	for _, ref := range refs {
		out = append(out, tssp.FrostSpentOutputRef{
			TxHash:      ref.TxHash,
			OutputIndex: ref.OutputIndex,
			KeyImage:    ref.KeyImage,
			SpendTxHash: ref.SpendTxHash,
		})
	}
	return out
}
