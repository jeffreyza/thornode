package thorclient

import (
	"errors"
	"testing"
)

func TestBroadcastApplicationErrorStrictness(t *testing.T) {
	if err := broadcastApplicationError(0, "", true); err != nil {
		t.Fatalf("success code must remain successful: %v", err)
	}
	if err := broadcastApplicationError(6, "unknown request", false); err != nil {
		t.Fatalf("generic broadcast must retain the legacy code-6 exception: %v", err)
	}
	err := broadcastApplicationError(6, "unknown request", true)
	if err == nil {
		t.Fatal("strict broadcast accepted code 6")
	}
	var rejection *ApplicationRejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("strict code 6 did not return a typed application rejection: %T", err)
	}
	if rejection.Code != 6 || rejection.Log != "unknown request" {
		t.Fatalf("unexpected typed rejection: code=%d log=%q", rejection.Code, rejection.Log)
	}
	if rejection.RetryableAfterAccountRefresh() {
		t.Fatal("code 6 application rejection was marked sequence-retryable")
	}
	if err = broadcastApplicationError(7, "invalid keygen", true); err == nil {
		t.Fatal("strict broadcast accepted another non-zero application code")
	}
	err = broadcastApplicationError(ABCIWrongSequenceCode, "bad sequence", false)
	if err == nil {
		t.Fatal("generic broadcast accepted a non-legacy application error")
	}
	if !errors.As(err, &rejection) || !rejection.RetryableAfterAccountRefresh() {
		t.Fatal("wrong-sequence rejection was not marked retryable after account refresh")
	}
}
