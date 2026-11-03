package monero

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A malformed pubkey filename can never become valid by retrying. With the
// production default of unlimited attempts (recoveryRetryMaxAttempts == 0), the
// old accounting counted it pending forever, so the recovery supervisor never
// terminated even after every real keyshare had restored. It must instead skip
// the artifact (retaining it on disk) and return.
func TestRestoreKeysharesTerminatesOnInvalidFilename(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Not a valid bech32 pubkey, so common.NewPubKey rejects it permanently.
	bad := filepath.Join(dir, "frost-recovery-not-a-valid-pubkey.hex")
	if err := os.WriteFile(bad, []byte("deadbeef"), 0o600); err != nil {
		t.Fatalf("write invalid artifact: %v", err)
	}

	// The invalid artifact is rejected before any sidecar call, so any non-nil
	// signer (needed only so restoreKeysharesFromDir does not early-return) works.
	c := newRecoveryTestClient(t, "http://127.0.0.1:1")
	// The production default: unlimited retries. A permanently-invalid artifact
	// must not turn that into an unterminating loop.
	c.recoveryRetryMaxAttempts = 0
	c.recoveryRetryInterval = time.Millisecond

	done := make(chan struct{})
	go func() {
		c.restoreKeysharesFromDir(dir)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("restoreKeysharesFromDir did not terminate on a permanently-invalid artifact; " +
			"a single malformed filename keeps the recovery supervisor alive for the process lifetime")
	}

	// The artifact is retained for operator inspection, not deleted.
	if _, err := os.Stat(bad); err != nil {
		t.Fatalf("invalid artifact must be retained on disk, stat err: %v", err)
	}
}
