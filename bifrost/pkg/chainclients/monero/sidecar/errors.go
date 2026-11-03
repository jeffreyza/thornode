package sidecar

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ErrUnavailableBlock indicates the requested block height is above the canonical chain tip.
var ErrUnavailableBlock = errors.New("sidecar chain block unavailable")

// maxResponseBytes caps the size of any response body the sidecar (or the
// monerod fallback) is allowed to return, to keep a misbehaving peer from
// exhausting memory.
const maxResponseBytes = 10 * 1024 * 1024

// Error responses are attacker-controlled and should never consume or enter
// logs at the successful-response limit.
const maxErrorResponseBytes = 8 * 1024

// HTTPError wraps a non-2xx response from the sidecar.
type HTTPError struct {
	Path             string
	Status           int
	BodyBytesRead    int
	BodyPrefixSHA256 string
	Truncated        bool
	Code             string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf(
		"sidecar endpoint %s returned status %d (code=%s body_bytes_read=%d body_prefix_sha256=%s truncated=%t)",
		e.Path,
		e.Status,
		e.Code,
		e.BodyBytesRead,
		e.BodyPrefixSHA256,
		e.Truncated,
	)
}

func newHTTPError(path string, status int, body []byte, truncated bool) *HTTPError {
	digest := sha256.Sum256(body)
	return &HTTPError{
		Path:             path,
		Status:           status,
		BodyBytesRead:    len(body),
		BodyPrefixSHA256: fmt.Sprintf("%x", digest[:]),
		Truncated:        truncated,
		Code:             sanitizedErrorCode(body),
	}
}

func sanitizedErrorCode(body []byte) string {
	var envelope struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Code) == 0 || len(envelope.Code) > 64 {
		return "unspecified"
	}
	for _, char := range envelope.Code {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return "unspecified"
		}
	}
	return envelope.Code
}

func remoteApplicationError(path, message string) error {
	digest := sha256.Sum256([]byte(message))
	return fmt.Errorf(
		"remote endpoint %s reported an application error (message_length=%d message_sha256=%x)",
		path,
		len(message),
		digest[:],
	)
}

// IsHTTPStatus reports whether err wraps an HTTPError with the given status code.
func IsHTTPStatus(err error, status int) bool {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	return httpErr.Status == status
}

func readBoundedResponseBody(body io.Reader, maxBytes int) ([]byte, error) {
	respBody, err := io.ReadAll(io.LimitReader(body, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(respBody) > maxBytes {
		return nil, fmt.Errorf("sidecar response exceeded %d byte limit", maxBytes)
	}
	return respBody, nil
}

func readBoundedErrorBody(body io.Reader) ([]byte, bool, error) {
	respBody, err := io.ReadAll(io.LimitReader(body, maxErrorResponseBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(respBody) > maxErrorResponseBytes {
		return respBody[:maxErrorResponseBytes], true, nil
	}
	return respBody, false, nil
}
