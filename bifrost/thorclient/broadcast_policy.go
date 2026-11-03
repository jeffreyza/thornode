package thorclient

import "fmt"

// ABCIWrongSequenceCode is the Cosmos SDK application code returned when the
// signed account sequence does not match the chain account sequence.
const ABCIWrongSequenceCode uint32 = 32

// ApplicationRejectionError reports a non-zero ABCI CheckTx response from a
// THORChain broadcast. A nil error from sync broadcast proves only zero-code
// CheckTx and is not evidence of DeliverTx execution or TssVoter persistence.
type ApplicationRejectionError struct {
	Code uint32
	Log  string
}

func (e *ApplicationRejectionError) Error() string {
	if e == nil {
		return "THORChain application rejection"
	}
	return fmt.Sprintf("fail to broadcast to THORChain,code:%d, log:%s", e.Code, e.Log)
}

// RetryableAfterAccountRefresh reports whether refreshing the signer account
// sequence and re-signing can make the same application message admissible.
func (e *ApplicationRejectionError) RetryableAfterAccountRefresh() bool {
	return e != nil && e.Code == ABCIWrongSequenceCode
}

// broadcastApplicationError applies the legacy generic code-6 exception only
// when strict semantics were not requested. Keeping this decision separate
// makes the security boundary directly testable without constructing a signed
// Cosmos transaction.
func broadcastApplicationError(code uint32, rawLog string, strict bool) error {
	if code == 0 || (!strict && code == 6) {
		return nil
	}
	return &ApplicationRejectionError{Code: code, Log: rawLog}
}
