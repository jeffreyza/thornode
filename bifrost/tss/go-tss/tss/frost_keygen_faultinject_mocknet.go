//go:build mocknet
// +build mocknet

package tss

import (
	"os"
	"strconv"
	"strings"
)

// frostKeygenTestRound3TimeoutMS reads the test-only round-3 wait override
// used by the xmr-keygen-recovery simulation (see `make test-simulation-xmr-keygen-recovery`).
// Returns 0 (no override) unless the environment variable holds a positive
// integer. Compiled into mocknet builds only; every other build uses the
// constant-zero stub, so the fault injection cannot exist in production
// binaries.
//
// NOTE: the variable travels under two names before reaching this reader - the
// Makefile exports XMR_KEYGEN_RECOVERY_ROUND3_TIMEOUT_MS and build/docker/docker-compose.yml
// bridges it to XMR_FROST_KEYGEN_TEST_ROUND3_TIMEOUT_MS (the name read below).
// Renaming either name in only one place silently disables the fault injection
// so keep all three in sync. The recovery actor also requires the logged
// override, actual timeout, and same-ceremony adoption.
func frostKeygenTestRound3TimeoutMS() int {
	v := strings.TrimSpace(os.Getenv("XMR_FROST_KEYGEN_TEST_ROUND3_TIMEOUT_MS"))
	if v == "" {
		return 0
	}
	ms, err := strconv.Atoi(v)
	if err != nil || ms <= 0 {
		return 0
	}
	return ms
}
