package thorclient

import (
	"fmt"
	"strings"

	"github.com/blang/semver"
)

func parseThorchainVersion(raw string) (semver.Version, error) {
	current, err := semver.Parse(strings.TrimSpace(raw))
	if err != nil {
		return semver.Version{}, fmt.Errorf("invalid THORChain current version %q: %w", raw, err)
	}
	return current, nil
}
