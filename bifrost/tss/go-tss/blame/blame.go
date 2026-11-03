package blame

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

func NewNode(pk string, blameData, blameSig []byte) Node {
	return Node{
		Pubkey:         pk,
		BlameData:      blameData,
		BlameSignature: blameSig,
	}
}

func (bn *Node) Equal(node Node) bool {
	if bn.Pubkey == node.Pubkey && bytes.Equal(bn.BlameSignature, node.BlameSignature) {
		return true
	}
	return false
}

// NewBlame create a new instance of Blame
func NewBlame(reason string, blameNodes []Node) Blame {
	return Blame{
		FailReason: reason,
		BlameNodes: blameNodes,
		blameLock:  &sync.RWMutex{},
	}
}

// String implement fmt.Stringer
func (b Blame) String() string {
	sb := strings.Builder{}
	sb.WriteString("reason:" + b.FailReason + " is_unicast:" + strconv.FormatBool(b.IsUnicast) + "\n")
	sb.WriteString(fmt.Sprintf("round:%s\n", b.Round))
	sb.WriteString(fmt.Sprintf("nodes:%+v\n", b.BlameNodes))
	return sb.String()
}

// SetBlame updates the field values of Blame, appending only new nodes (by
// pubkey+signature equality, the same equality AddBlameNodes uses). This is
// important under tss-lib v3 where multiple in-flight signing messages can
// each trigger SetBlame with the same offending peer set — without dedup the
// resulting BlameNodes slice would grow several entries per actual culprit.
func (b *Blame) SetBlame(reason string, nodes []Node, isUnicast bool, round string) {
	b.blameLock.Lock()
	defer b.blameLock.Unlock()
	b.FailReason = reason
	b.IsUnicast = isUnicast
	b.Round = round
	for _, node := range nodes {
		found := false
		for _, el := range b.BlameNodes {
			if node.Equal(el) {
				found = true
				break
			}
		}
		if !found {
			b.BlameNodes = append(b.BlameNodes, node)
		}
	}
}

func (b *Blame) AlreadyBlame() bool {
	b.blameLock.RLock()
	defer b.blameLock.RUnlock()
	return len(b.BlameNodes) > 0
}

// GetFailReason returns the current fail reason under the read lock.
func (b *Blame) GetFailReason() string {
	b.blameLock.RLock()
	defer b.blameLock.RUnlock()
	return b.FailReason
}

// BlameNodesCount returns the number of blamed nodes under the read lock.
func (b *Blame) BlameNodesCount() int {
	b.blameLock.RLock()
	defer b.blameLock.RUnlock()
	return len(b.BlameNodes)
}

// SetIsUnicast sets the unicast flag under the write lock.
func (b *Blame) SetIsUnicast(isUnicast bool) {
	b.blameLock.Lock()
	defer b.blameLock.Unlock()
	b.IsUnicast = isUnicast
}

// SetRound sets the round under the write lock.
func (b *Blame) SetRound(round string) {
	b.blameLock.Lock()
	defer b.blameLock.Unlock()
	b.Round = round
}

// Snapshot returns a consistent copy with an independent lock and node slice.
func (b *Blame) Snapshot() Blame {
	b.blameLock.RLock()
	defer b.blameLock.RUnlock()

	nodes := make([]Node, len(b.BlameNodes))
	copy(nodes, b.BlameNodes)
	return Blame{
		FailReason: b.FailReason,
		IsUnicast:  b.IsUnicast,
		Round:      b.Round,
		BlameNodes: nodes,
		blameLock:  &sync.RWMutex{},
	}
}

// AddBlameNodes add nodes to the blame list
func (b *Blame) AddBlameNodes(newBlameNodes ...Node) {
	b.blameLock.Lock()
	defer b.blameLock.Unlock()
	for _, node := range newBlameNodes {
		found := false
		for _, el := range b.BlameNodes {
			if node.Equal(el) {
				found = true
				break
			}
		}
		if !found {
			b.BlameNodes = append(b.BlameNodes, node)
		}
	}
}
