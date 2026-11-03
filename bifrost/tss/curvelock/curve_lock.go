// Package curvelock serializes mutations of tss-lib's process-global elliptic
// curve. v3's ECPoint.UnmarshalJSON reads the embedded "Curve" field when
// present and otherwise falls back to tss.EC(); legacy on-disk keyshares may
// lack the field, so callers must pin the expected curve before unmarshalling.
//
// This lives in its own leaf package (no project dependencies) so any caller
// that touches keyshare deserialisation — keysign, eddsa, p2p/storage — can
// hold the same mutex without dragging in larger packages.
package curvelock

import (
	"crypto/elliptic"
	"sync"

	btss "gitlab.com/thorchain/tss/tss-lib-private.git/v3/tss"
)

var curveMu sync.Mutex

// WithCurve sets the tss-lib global curve and runs fn under a shared mutex.
//
// The lock covers the entire fn invocation — typically a json.Unmarshal of a
// multi-MB LocalPartySaveData. Concurrent keysign (ECDSA-vs-ECDSA and
// ECDSA-vs-EdDSA) therefore serialize their unmarshal phase. This is correct
// because tss-lib's global curve must remain pinned for the duration of any
// ECPoint UnmarshalJSON that falls back to btss.EC().
//
// Once fn returns each parsed ECPoint has captured its own curve, so further
// operations on the parsed value do not depend on the global.
//
// If contention becomes visible in profiles, the fast path is to skip the
// lock when the keyshare carries an embedded "Curve" field on every ECPoint
// (true for v3 fixtures, false for legacy). See REMOVAL.md.
func WithCurve(c elliptic.Curve, fn func() error) error {
	curveMu.Lock()
	defer curveMu.Unlock()
	prev := btss.EC()
	btss.SetCurve(c)
	defer btss.SetCurve(prev)
	return fn()
}
