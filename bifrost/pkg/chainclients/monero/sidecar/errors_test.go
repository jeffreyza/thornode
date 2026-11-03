package sidecar

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// failingErrBodyReader yields prefix bytes then fails, exercising the read-error
// branches of the bounded body readers.
type failingErrBodyReader struct {
	prefix []byte
	pos    int
}

func (r *failingErrBodyReader) Read(p []byte) (int, error) {
	if r.pos < len(r.prefix) {
		n := copy(p, r.prefix[r.pos:])
		r.pos += n
		return n, nil
	}
	return 0, errors.New("xmr-errcode-test: simulated transport failure")
}

// TestSanitizedErrorCodeHostileBodies pins the sanitizer that decides which
// attacker-controlled bytes are allowed to reach logs as an error "code".
func TestSanitizedErrorCodeHostileBodies(t *testing.T) {
	t.Parallel()

	// exactly at the 64-byte cap enforced by sanitizedErrorCode
	codeAt64 := strings.Repeat("a", 64)
	codeOver64 := strings.Repeat("a", 65)

	// 60 ASCII bytes + a 4-byte emoji == 64 bytes, so it passes the byte-length
	// gate and must be rejected by the character-class allowlist instead.
	multibyteAtCap := strings.Repeat("a", 60) + "\U0001F600"
	if len(multibyteAtCap) != 64 {
		t.Fatalf("test fixture invariant broken: want 64 bytes, got %d", len(multibyteAtCap))
	}

	cases := []struct {
		name string
		body string
		want string
	}{
		// --- allowlist: accepted shapes ---
		{name: "lowercase_letters", body: `{"code":"badrequest"}`, want: "badrequest"},
		{name: "digits", body: `{"code":"12345"}`, want: "12345"},
		{name: "underscore_and_hyphen", body: `{"code":"block_not-found9"}`, want: "block_not-found9"},
		{name: "exactly_64_bytes_allowed", body: `{"code":"` + codeAt64 + `"}`, want: codeAt64},
		{name: "extra_fields_ignored", body: `{"code":"nope","detail":"whatever"}`, want: "nope"},
		{name: "duplicate_code_key_last_wins", body: `{"code":"aaa","code":"bbb"}`, want: "bbb"},

		// --- allowlist: rejected character classes ---
		{name: "uppercase_rejected", body: `{"code":"BadRequest"}`, want: "unspecified"},
		{name: "space_rejected", body: `{"code":"bad request"}`, want: "unspecified"},
		{name: "dot_rejected", body: `{"code":"bad.request"}`, want: "unspecified"},
		{name: "slash_rejected", body: `{"code":"bad/../../etc/passwd"}`, want: "unspecified"},
		{name: "escaped_ansi_escape_rejected", body: `{"code":"a\` + `u001b[31mred"}`, want: "unspecified"},
		{name: "escaped_newline_rejected", body: `{"code":"line1\nline2"}`, want: "unspecified"},
		{name: "escaped_crlf_log_injection_rejected", body: `{"code":"ok\r\nFATAL fake log line"}`, want: "unspecified"},
		{name: "escaped_tab_rejected", body: `{"code":"a\tb"}`, want: "unspecified"},
		{name: "escaped_nul_rejected", body: `{"code":"a\` + `u0000b"}`, want: "unspecified"},
		{name: "raw_control_byte_is_invalid_json", body: "{\"code\":\"a\nb\"}", want: "unspecified"},

		// --- non-ASCII ---
		{name: "non_ascii_latin1_rejected", body: `{"code":"café"}`, want: "unspecified"},
		{name: "non_ascii_cyrillic_homoglyph_rejected", body: `{"code":"аbc"}`, want: "unspecified"},
		{name: "emoji_rejected", body: `{"code":"😀"}`, want: "unspecified"},
		{name: "invalid_utf8_bytes_rejected", body: "{\"code\":\"\xff\xfeabc\"}", want: "unspecified"},
		{name: "multibyte_at_byte_cap_rejected", body: `{"code":"` + multibyteAtCap + `"}`, want: "unspecified"},

		// --- length cap ---
		{name: "65_bytes_rejected", body: `{"code":"` + codeOver64 + `"}`, want: "unspecified"},
		{name: "very_long_code_rejected", body: `{"code":"` + strings.Repeat("z", 4096) + `"}`, want: "unspecified"},

		// --- empty / missing / malformed JSON ---
		{name: "empty_body", body: "", want: "unspecified"},
		{name: "empty_code_string", body: `{"code":""}`, want: "unspecified"},
		{name: "missing_code_field", body: `{"message":"boom"}`, want: "unspecified"},
		{name: "json_null", body: `null`, want: "unspecified"},
		{name: "json_array", body: `[]`, want: "unspecified"},
		{name: "code_is_number", body: `{"code":5}`, want: "unspecified"},
		{name: "code_is_object", body: `{"code":{"a":1}}`, want: "unspecified"},
		{name: "not_json_at_all", body: `<html>502 Bad Gateway</html>`, want: "unspecified"},
		{name: "truncated_json", body: `{"code":"abc`, want: "unspecified"},
		{name: "trailing_garbage_after_json", body: `{"code":"abc"} <script>`, want: "unspecified"},
		{name: "whitespace_only", body: "   \n\t ", want: "unspecified"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sanitizedErrorCode([]byte(tc.body)); got != tc.want {
				t.Fatalf("sanitizedErrorCode(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// TestSanitizedErrorCodeNeverEscapesAllowlist is a broad guard: whatever comes
// back must always be either the literal "unspecified" sentinel or a string made
// solely of [a-z0-9_-] and no longer than 64 bytes.
func TestSanitizedErrorCodeNeverEscapesAllowlist(t *testing.T) {
	t.Parallel()

	bodies := []string{
		`{"code":"ok"}`,
		`{"code":"BAD"}`,
		`{"code":"` + strings.Repeat("a", 65) + `"}`,
		`{"code":"a\` + `u001b]0;pwn"}`,
		"{\"code\":\"\xff\"}",
		"",
		"garbage",
	}
	for _, body := range bodies {
		got := sanitizedErrorCode([]byte(body))
		if got == "unspecified" {
			continue
		}
		if len(got) == 0 || len(got) > 64 {
			t.Fatalf("sanitizedErrorCode(%q) returned out-of-bounds length %d (%q)", body, len(got), got)
		}
		for _, r := range got {
			ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
			if !ok {
				t.Fatalf("sanitizedErrorCode(%q) leaked disallowed rune %q in %q", body, r, got)
			}
		}
	}
}

// TestReadBoundedErrorBodyBounds pins the 8 KiB error-body bound and the
// truncated flag exactly at, one under, and one over the limit.
func TestReadBoundedErrorBodyBounds(t *testing.T) {
	t.Parallel()

	if maxErrorResponseBytes != 8*1024 {
		t.Fatalf("maxErrorResponseBytes changed: got %d, want 8192", maxErrorResponseBytes)
	}

	cases := []struct {
		name          string
		size          int
		wantLen       int
		wantTruncated bool
	}{
		{name: "empty", size: 0, wantLen: 0, wantTruncated: false},
		{name: "one_byte_under_bound", size: maxErrorResponseBytes - 1, wantLen: maxErrorResponseBytes - 1, wantTruncated: false},
		{name: "exactly_at_bound", size: maxErrorResponseBytes, wantLen: maxErrorResponseBytes, wantTruncated: false},
		{name: "one_byte_over_bound", size: maxErrorResponseBytes + 1, wantLen: maxErrorResponseBytes, wantTruncated: true},
		{name: "far_over_bound", size: 5 * maxErrorResponseBytes, wantLen: maxErrorResponseBytes, wantTruncated: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Distinct byte pattern so truncation is positionally verifiable.
			src := make([]byte, tc.size)
			for i := range src {
				src[i] = byte('a' + i%26)
			}

			got, truncated, err := readBoundedErrorBody(bytes.NewReader(src))
			if err != nil {
				t.Fatalf("readBoundedErrorBody(size=%d) unexpected error: %v", tc.size, err)
			}
			if truncated != tc.wantTruncated {
				t.Fatalf("readBoundedErrorBody(size=%d) truncated = %t, want %t", tc.size, truncated, tc.wantTruncated)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("readBoundedErrorBody(size=%d) len = %d, want %d", tc.size, len(got), tc.wantLen)
			}
			if !bytes.Equal(got, src[:tc.wantLen]) {
				t.Fatalf("readBoundedErrorBody(size=%d) returned bytes that are not the source prefix", tc.size)
			}
		})
	}
}

// TestReadBoundedErrorBodyReadFailure ensures transport failures surface rather
// than being reported as a clean, empty, non-truncated body.
func TestReadBoundedErrorBodyReadFailure(t *testing.T) {
	t.Parallel()

	body, truncated, err := readBoundedErrorBody(&failingErrBodyReader{prefix: []byte("partial")})
	if err == nil {
		t.Fatalf("readBoundedErrorBody: expected error, got body=%q truncated=%t", body, truncated)
	}
	if body != nil {
		t.Fatalf("readBoundedErrorBody: expected nil body on error, got %q", body)
	}
	if truncated {
		t.Fatalf("readBoundedErrorBody: expected truncated=false on error")
	}
}

// TestReadBoundedResponseBodyLimit covers the success-path limiter, which
// rejects (rather than truncates) anything over its bound.
func TestReadBoundedResponseBodyLimit(t *testing.T) {
	t.Parallel()

	const limit = 16

	cases := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{name: "under_limit", size: limit - 1, wantErr: false},
		{name: "exactly_at_limit", size: limit, wantErr: false},
		{name: "one_over_limit", size: limit + 1, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := readBoundedResponseBody(bytes.NewReader(bytes.Repeat([]byte("x"), tc.size)), limit)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("readBoundedResponseBody(size=%d) expected error, got %d bytes", tc.size, len(got))
				}
				if got != nil {
					t.Fatalf("readBoundedResponseBody(size=%d) expected nil body on error", tc.size)
				}
				return
			}
			if err != nil {
				t.Fatalf("readBoundedResponseBody(size=%d) unexpected error: %v", tc.size, err)
			}
			if len(got) != tc.size {
				t.Fatalf("readBoundedResponseBody(size=%d) len = %d", tc.size, len(got))
			}
		})
	}
}

// TestNewHTTPErrorHostileBodyStaysOutOfMessage is the end-to-end property the
// two helpers above exist to guarantee: no attacker-controlled byte of the
// response body may appear verbatim in the formatted error string.
func TestNewHTTPErrorHostileBodyStaysOutOfMessage(t *testing.T) {
	t.Parallel()

	hostile := []byte("{\"code\":\"OK\\u001b[2J\",\"secret\":\"hunter2\"}\nFATAL forged log line\n")

	httpErr := newHTTPError("/v1/monero/chain/height", 503, hostile, true)
	if httpErr.Code != "unspecified" {
		t.Fatalf("newHTTPError Code = %q, want %q", httpErr.Code, "unspecified")
	}
	if httpErr.BodyBytesRead != len(hostile) {
		t.Fatalf("newHTTPError BodyBytesRead = %d, want %d", httpErr.BodyBytesRead, len(hostile))
	}
	if !httpErr.Truncated {
		t.Fatalf("newHTTPError dropped the truncated flag")
	}
	if len(httpErr.BodyPrefixSHA256) != 64 {
		t.Fatalf("newHTTPError BodyPrefixSHA256 = %q, want 64 hex chars", httpErr.BodyPrefixSHA256)
	}

	msg := httpErr.Error()
	for _, leak := range []string{"hunter2", "FATAL forged log line", "\x1b", "\n"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("HTTPError.Error() leaked %q from the hostile body: %q", leak, msg)
		}
	}
	if !strings.Contains(msg, "code=unspecified") || !strings.Contains(msg, "truncated=true") {
		t.Fatalf("HTTPError.Error() missing expected fields: %q", msg)
	}

	// A benign, allowlisted code must survive round-trip.
	benign := newHTTPError("/v1/monero/chain/block", 404, []byte(`{"code":"block_unavailable"}`), false)
	if benign.Code != "block_unavailable" {
		t.Fatalf("newHTTPError Code = %q, want %q", benign.Code, "block_unavailable")
	}
	if !IsHTTPStatus(benign, 404) || IsHTTPStatus(benign, 500) {
		t.Fatalf("IsHTTPStatus misclassified status %d", benign.Status)
	}
	if IsHTTPStatus(errors.New("not an http error"), 404) {
		t.Fatalf("IsHTTPStatus matched a non-HTTPError")
	}
}

// TestRemoteApplicationErrorHidesMessage pins that remote application error text
// is reduced to a length + digest and never echoed.
func TestRemoteApplicationErrorHidesMessage(t *testing.T) {
	t.Parallel()

	secret := "wallet password is hunter2"
	err := remoteApplicationError("monerod-json-rpc/-32601", secret)
	msg := err.Error()
	if strings.Contains(msg, secret) || strings.Contains(msg, "hunter2") {
		t.Fatalf("remoteApplicationError leaked the message: %q", msg)
	}
	if !strings.Contains(msg, "message_length=26") {
		t.Fatalf("remoteApplicationError missing message_length: %q", msg)
	}
}

var _ io.Reader = (*failingErrBodyReader)(nil)
