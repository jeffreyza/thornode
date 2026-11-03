package cosmos

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIsCosmosAccountNotFoundError(t *testing.T) {
	t.Run("matches grpc not found", func(t *testing.T) {
		err := status.Error(codes.NotFound, "account cosmos1abc not found")
		if !isCosmosAccountNotFoundError(err) {
			t.Fatalf("expected grpc not found to be treated as missing account")
		}
	})

	t.Run("ignores unrelated errors", func(t *testing.T) {
		err := errors.New("rpc unavailable")
		if isCosmosAccountNotFoundError(err) {
			t.Fatalf("expected unrelated error to remain fatal")
		}
	})
}
