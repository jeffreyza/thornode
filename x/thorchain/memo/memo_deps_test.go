package thorchain

import (
	"os/exec"
	"strings"
	"testing"
)

// TestMemoDoesNotImportTssLib keeps the memo parser buildable from public modules only.
// tss-lib-private is not public, so a package that imports it cannot be built by
// outside fuzzing and verification tools (for example OSS Verify, which builds
// FuzzParseMemo anonymously). TSS round constants that consensus code needs belong in
// bifrost/p2p/tssrounds, not bifrost/p2p/messages.
func TestMemoDoesNotImportTssLib(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go command not on PATH")
	}
	out, err := exec.Command("go", "list", "-deps", "-test", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.Contains(pkg, "tss-lib") {
			t.Fatalf("x/thorchain/memo depends on %s; keep tss-lib out of the memo parser's imports", pkg)
		}
	}
}
