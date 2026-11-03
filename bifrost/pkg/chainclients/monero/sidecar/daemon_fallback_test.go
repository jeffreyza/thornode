package sidecar

import (
	"strings"
	"testing"
)

// TestParseDaemonEndpointURLValid pins the accepted daemon endpoint shapes,
// including the implicit http:// prefixing of a bare host:port.
func TestParseDaemonEndpointURLValid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		endpoint     string
		wantScheme   string
		wantHost     string
		wantHostname string
		wantPath     string
	}{
		{
			name:         "explicit_http_with_port",
			endpoint:     "http://monerod:18081",
			wantScheme:   "http",
			wantHost:     "monerod:18081",
			wantHostname: "monerod",
		},
		{
			name:         "explicit_https",
			endpoint:     "https://monerod.example.com",
			wantScheme:   "https",
			wantHost:     "monerod.example.com",
			wantHostname: "monerod.example.com",
		},
		{
			name:         "bare_host_port_gets_http_prefix",
			endpoint:     "127.0.0.1:18081",
			wantScheme:   "http",
			wantHost:     "127.0.0.1:18081",
			wantHostname: "127.0.0.1",
		},
		{
			name:         "surrounding_whitespace_trimmed",
			endpoint:     "  http://monerod:18081  ",
			wantScheme:   "http",
			wantHost:     "monerod:18081",
			wantHostname: "monerod",
		},
		{
			name:         "path_preserved",
			endpoint:     "http://monerod:18081/json_rpc",
			wantScheme:   "http",
			wantHost:     "monerod:18081",
			wantHostname: "monerod",
			wantPath:     "/json_rpc",
		},
		{
			name:         "ipv6_literal",
			endpoint:     "http://[::1]:18081",
			wantScheme:   "http",
			wantHost:     "[::1]:18081",
			wantHostname: "::1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := parseDaemonEndpointURL(tc.endpoint)
			if err != nil {
				t.Fatalf("parseDaemonEndpointURL(%q) unexpected error: %v", tc.endpoint, err)
			}
			if parsed.Scheme != tc.wantScheme {
				t.Fatalf("scheme = %q, want %q", parsed.Scheme, tc.wantScheme)
			}
			if parsed.Host != tc.wantHost {
				t.Fatalf("host = %q, want %q", parsed.Host, tc.wantHost)
			}
			if parsed.Hostname() != tc.wantHostname {
				t.Fatalf("hostname = %q, want %q", parsed.Hostname(), tc.wantHostname)
			}
			if parsed.Path != tc.wantPath {
				t.Fatalf("path = %q, want %q", parsed.Path, tc.wantPath)
			}
			if parsed.User != nil {
				t.Fatalf("unexpected userinfo on a credential-free endpoint: %v", parsed.User)
			}
		})
	}
}

// TestParseDaemonEndpointURLRejected pins the hostile / malformed inputs the
// parser exists to reject: embedded URL credentials, query strings and
// fragments (which would otherwise be carried into every /json_rpc call).
func TestParseDaemonEndpointURLRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		endpoint    string
		wantErrPart string
	}{
		// --- embedded credentials ---
		{
			name:        "userinfo_with_password",
			endpoint:    "http://user:hunter2@monerod:18081",
			wantErrPart: "must not contain URL credentials",
		},
		{
			name:        "userinfo_with_password_https",
			endpoint:    "https://user:hunter2@monerod:18081",
			wantErrPart: "must not contain URL credentials",
		},
		{
			name:        "userinfo_username_only",
			endpoint:    "http://user@monerod:18081",
			wantErrPart: "must not contain URL credentials",
		},
		{
			name:        "userinfo_without_scheme_still_rejected",
			endpoint:    "user:hunter2@monerod:18081",
			wantErrPart: "must not contain URL credentials",
		},
		{
			name:        "userinfo_empty_password",
			endpoint:    "http://user:@monerod:18081",
			wantErrPart: "must not contain URL credentials",
		},
		{
			name:        "host_confusion_via_userinfo",
			endpoint:    "http://monerod.internal@evil.example.com",
			wantErrPart: "must not contain URL credentials",
		},

		// --- query strings ---
		{
			name:        "query_string",
			endpoint:    "http://monerod:18081/json_rpc?admin=1",
			wantErrPart: "must not contain a query or fragment",
		},
		{
			name:        "query_string_on_root",
			endpoint:    "http://monerod:18081?x=y",
			wantErrPart: "must not contain a query or fragment",
		},
		{
			name:        "force_query_bare_question_mark",
			endpoint:    "http://monerod:18081?",
			wantErrPart: "must not contain a query or fragment",
		},

		// --- fragments ---
		{
			name:        "fragment",
			endpoint:    "http://monerod:18081#frag",
			wantErrPart: "must not contain a query or fragment",
		},
		{
			name:        "fragment_with_path",
			endpoint:    "http://monerod:18081/json_rpc#frag",
			wantErrPart: "must not contain a query or fragment",
		},

		// --- empty / hostless / unparsable ---
		{
			name:        "empty",
			endpoint:    "",
			wantErrPart: "is empty",
		},
		{
			name:        "whitespace_only",
			endpoint:    "   \t  ",
			wantErrPart: "is empty",
		},
		{
			name:        "scheme_only_no_host",
			endpoint:    "http://",
			wantErrPart: "invalid monero rpc host",
		},
		{
			name:        "empty_host_with_path",
			endpoint:    "http:///json_rpc",
			wantErrPart: "invalid monero rpc host",
		},
		{
			name:        "unparsable_url",
			endpoint:    "http:monerod",
			wantErrPart: "invalid monero rpc host URL",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := parseDaemonEndpointURL(tc.endpoint)
			if err == nil {
				t.Fatalf("parseDaemonEndpointURL(%q) = %v, want error", tc.endpoint, parsed)
			}
			if parsed != nil {
				t.Fatalf("parseDaemonEndpointURL(%q) returned a non-nil URL alongside an error", tc.endpoint)
			}
			if !strings.Contains(err.Error(), tc.wantErrPart) {
				t.Fatalf("parseDaemonEndpointURL(%q) error = %q, want it to contain %q", tc.endpoint, err, tc.wantErrPart)
			}
		})
	}
}

// TestParseDaemonEndpointURLErrorNeverLeaksCredentials guards the comment in
// parseDaemonEndpointURL: the rejection error must not echo the offending URL
// (an operator may have pasted a real password into it).
func TestParseDaemonEndpointURLErrorNeverLeaksCredentials(t *testing.T) {
	t.Parallel()

	const password = "sup3rs3cr3tpassw0rd"
	endpoints := []string{
		"http://admin:" + password + "@monerod:18081",
		"admin:" + password + "@monerod:18081",
		// Malformed on purpose: forces the url.Parse failure branch, which must
		// not wrap the *url.Error (that struct embeds the full input).
		"http://admin:" + password + "@monerod:notaport",
		"ht tp://admin:" + password + "@monerod",
	}

	for _, endpoint := range endpoints {
		_, err := parseDaemonEndpointURL(endpoint)
		if err == nil {
			t.Fatalf("parseDaemonEndpointURL(%q) unexpectedly succeeded", endpoint)
		}
		msg := err.Error()
		if strings.Contains(msg, password) {
			t.Fatalf("parseDaemonEndpointURL leaked the password in %q", msg)
		}
		if strings.Contains(msg, "admin") {
			t.Fatalf("parseDaemonEndpointURL leaked the username in %q", msg)
		}
	}
}

// A scheme is only defaulted when the endpoint has none, so the scheme
// allowlist is reachable: a case-variant http(s) scheme resolves to the host the
// operator meant, and any other scheme is rejected rather than being mangled
// into a bogus hostname and accepted.
func TestParseDaemonEndpointURLSchemeAllowlistIsEnforced(t *testing.T) {
	t.Parallel()

	accepted := []struct {
		name       string
		endpoint   string
		wantScheme string
	}{
		{name: "uppercase_https_scheme", endpoint: "HTTPS://monerod:18081", wantScheme: "https"},
		{name: "mixed_case_http_scheme", endpoint: "Http://monerod:18081", wantScheme: "http"},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := parseDaemonEndpointURL(tc.endpoint)
			if err != nil {
				t.Fatalf("parseDaemonEndpointURL(%q): %v", tc.endpoint, err)
			}
			if parsed.Scheme != tc.wantScheme {
				t.Fatalf("scheme = %q, want %q", parsed.Scheme, tc.wantScheme)
			}
			if parsed.Hostname() != "monerod" {
				t.Fatalf("hostname = %q, want %q: the endpoint was mangled", parsed.Hostname(), "monerod")
			}
		})
	}

	rejected := []struct {
		name     string
		endpoint string
	}{
		{name: "ftp_scheme", endpoint: "ftp://monerod"},
		{name: "file_scheme", endpoint: "file:///etc/passwd"},
		{name: "uppercase_ftp_scheme", endpoint: "FTP://monerod"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := parseDaemonEndpointURL(tc.endpoint); err == nil {
				t.Fatalf("parseDaemonEndpointURL(%q) accepted a non-http scheme", tc.endpoint)
			}
		})
	}
}

// TestNormalizeDaemonEndpointTrimsRPCSuffix pins the endpoint normalization that
// daemonRPC relies on when it appends "/json_rpc".
func TestNormalizeDaemonEndpointTrimsRPCSuffix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		endpoint string
		want     string
	}{
		{name: "plain_host", endpoint: "http://monerod:18081", want: "http://monerod:18081"},
		{name: "trailing_slash", endpoint: "http://monerod:18081/", want: "http://monerod:18081"},
		{name: "json_rpc_suffix", endpoint: "http://monerod:18081/json_rpc", want: "http://monerod:18081"},
		{name: "json_rpc_suffix_trailing_slash", endpoint: "http://monerod:18081/json_rpc/", want: "http://monerod:18081"},
		{name: "https_with_base_path", endpoint: "https://monerod.example.com/xmr", want: "https://monerod.example.com/xmr"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := parseDaemonEndpointURL(tc.endpoint)
			if err != nil {
				t.Fatalf("parseDaemonEndpointURL(%q) unexpected error: %v", tc.endpoint, err)
			}
			if got := normalizeDaemonEndpoint(parsed); got != tc.want {
				t.Fatalf("normalizeDaemonEndpoint(%q) = %q, want %q", tc.endpoint, got, tc.want)
			}
		})
	}

	if got := normalizeDaemonEndpoint(nil); got != "" {
		t.Fatalf("normalizeDaemonEndpoint(nil) = %q, want empty string", got)
	}
}

// TestDaemonRPCErrorHidesRemoteMessage pins that a hostile daemon JSON-RPC error
// message is never echoed verbatim into the error string.
func TestDaemonRPCErrorHidesRemoteMessage(t *testing.T) {
	t.Parallel()

	var nilErr *daemonRPCError
	if got := nilErr.Error(); got != "" {
		t.Fatalf("(*daemonRPCError)(nil).Error() = %q, want empty", got)
	}

	blank := &daemonRPCError{Code: -32601, Message: "   "}
	if got := blank.Error(); got != "daemon rpc error -32601" {
		t.Fatalf("blank-message daemonRPCError.Error() = %q", got)
	}

	hostile := &daemonRPCError{Code: -1, Message: "wallet seed: abandon abandon abandon\nFATAL forged"}
	msg := hostile.Error()
	for _, leak := range []string{"abandon", "wallet seed", "FATAL forged", "\n"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("daemonRPCError.Error() leaked %q: %q", leak, msg)
		}
	}
	if !strings.Contains(msg, "message_sha256=") || !strings.Contains(msg, "monerod-json-rpc/-1") {
		t.Fatalf("daemonRPCError.Error() missing expected fields: %q", msg)
	}
}
