package thorclient

import "testing"

func TestParseThorchainVersion(t *testing.T) {
	version, err := parseThorchainVersion(" 3.20.0 ")
	if err != nil {
		t.Fatalf("valid current version was rejected: %v", err)
	}
	if version.String() != "3.20.0" {
		t.Fatalf("unexpected parsed version %s", version)
	}

	for _, raw := range []string{"", "   ", "not-semver"} {
		if _, err = parseThorchainVersion(raw); err == nil {
			t.Fatalf("invalid current version %q was accepted", raw)
		}
	}
}
