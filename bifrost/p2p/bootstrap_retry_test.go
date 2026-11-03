package p2p

import "time"

// Several suites start hosts whose bootstrap peers are unreachable on purpose;
// keep every retry round but not the real 5s wait between them.
func init() {
	bootstrapRetrySleep = 200 * time.Millisecond
}
