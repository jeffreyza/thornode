package xrp

import (
	"context"
	"errors"
	"testing"

	txqueries "github.com/Peersyst/xrpl-go/xrpl/queries/transactions"
)

func TestIsAccountNotFoundXRPError(t *testing.T) {
	t.Run("matches xrpl actNotFound", func(t *testing.T) {
		err := errors.New("actNotFound")
		if !isAccountNotFoundXRPError(err) {
			t.Fatalf("expected actNotFound to be treated as missing account")
		}
	})

	t.Run("ignores unrelated errors", func(t *testing.T) {
		err := errors.New("tefPAST_SEQ")
		if isAccountNotFoundXRPError(err) {
			t.Fatalf("expected unrelated xrpl error to remain fatal")
		}
	})
}

func TestIsAcceptedXRPSubmitResponse(t *testing.T) {
	t.Run("accepts successful submit", func(t *testing.T) {
		if !isAcceptedXRPSubmitResponse(&txqueries.SubmitResponse{EngineResult: "tesSUCCESS"}) {
			t.Fatalf("expected tesSUCCESS to be accepted")
		}
	})

	t.Run("accepts past sequence replay", func(t *testing.T) {
		if !isAcceptedXRPSubmitResponse(&txqueries.SubmitResponse{EngineResult: "tefPAST_SEQ"}) {
			t.Fatalf("expected tefPAST_SEQ to be treated as already submitted")
		}
	})

	t.Run("accepts queued submit", func(t *testing.T) {
		if !isAcceptedXRPSubmitResponse(&txqueries.SubmitResponse{EngineResult: "terQUEUED", Queued: true}) {
			t.Fatalf("expected queued response to be accepted")
		}
	})

	t.Run("rejects unrelated engine result", func(t *testing.T) {
		if isAcceptedXRPSubmitResponse(&txqueries.SubmitResponse{EngineResult: "temMALFORMED"}) {
			t.Fatalf("expected malformed response to remain fatal")
		}
	})
}

func TestIsRetryableXRPError(t *testing.T) {
	t.Run("matches context deadline", func(t *testing.T) {
		if !isRetryableXRPError(context.DeadlineExceeded) {
			t.Fatalf("expected context deadline exceeded to be retryable")
		}
	})

	t.Run("matches txn not found", func(t *testing.T) {
		if !isRetryableXRPError(errors.New("txnNotFound")) {
			t.Fatalf("expected txnNotFound to be retryable")
		}
	})

	t.Run("ignores permanent error", func(t *testing.T) {
		if isRetryableXRPError(errors.New("temMALFORMED")) {
			t.Fatalf("expected permanent xrpl error to remain fatal")
		}
	})
}
