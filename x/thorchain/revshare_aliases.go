package thorchain

import "strings"

// revShareThornameAliases bridges affiliate thornames whose characters are
// illegal in a Mimir key to a Mimir-safe alias token. Mimir keys allow only
// [a-zA-Z0-9-] (constants.MimirKeyRegex); thornames additionally allow '_' and
// '+'. So an affiliate like SwapKit, whose thorname is "-_", can never appear
// verbatim in a `REVSHARE-<thorname>` Mimir key. Nodes instead vote on
// `REVSHARE-SWAPKIT`, and this map resolves that alias back to the real
// thorname at both the validation and payout sites.
//
// Keys are UPPER-CASE thornames (the canonical form used by the rev-share
// accumulator and handler_mimir validation). Values are UPPER-CASE alias
// tokens and MUST be unique and Mimir-key-safe.
//
// ponytail: hardcoded map because only a couple of affiliates have
// non-Mimir-safe thornames. If this list needs to grow or change without a
// binary release, promote it to a kvstore + tx type (see the Discord thread —
// this is the "hardcoded thingy just for swapkit").
var revShareThornameAliases = map[string]string{
	"-_": "SWAPKIT",
}

// revShareAliasToThornameMap is the reverse of revShareThornameAliases (alias
// token -> thorname). Kept as its own literal so lookups never iterate a map
// (the determinism linter forbids map iteration). Keep in sync with the map above.
var revShareAliasToThornameMap = map[string]string{
	"SWAPKIT": "-_",
}

// revShareThornameToAlias returns the Mimir-safe alias token for a thorname, or
// the thorname unchanged when it has no alias. Used on the payout side to build
// the `REVSHARE-<token>` key that nodes actually voted on. Input/output upper-case.
func revShareThornameToAlias(thorname string) string {
	if alias, ok := revShareThornameAliases[strings.ToUpper(thorname)]; ok {
		return alias
	}
	return thorname
}

// revShareAliasToThorname reverses revShareThornameToAlias: given the token
// after the `REVSHARE-` prefix, it returns the real thorname when the token is a
// known alias, else the token unchanged. Used on the validation side to check
// the aliased thorname actually exists. Input/output upper-case.
func revShareAliasToThorname(token string) string {
	up := strings.ToUpper(token)
	if thorname, ok := revShareAliasToThornameMap[up]; ok {
		return thorname
	}
	return up
}
