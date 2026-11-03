package p2p

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
)

// LeaderNode use the given input buf to calculate a hash , and consistently choose a node as a master coordinate note
func LeaderNode(msgID string, blockHeight int64, pIDs []string) (string, error) {
	leaders, err := LeaderNodes(msgID, blockHeight, pIDs)
	if err != nil {
		return "", err
	}
	return leaders[0], nil
}

// LeaderNodes returns the complete deterministic coordinator order for a
// party. LeaderNode remains the first candidate for compatibility; protocols
// which can safely retry party formation may advance through this order
// without relying on unsynchronised local retry counters.
func LeaderNodes(msgID string, blockHeight int64, pIDs []string) ([]string, error) {
	if len(pIDs) == 0 || len(msgID) == 0 || blockHeight == 0 {
		return nil, errors.New("invalid input for finding the leader")
	}
	type candidate struct {
		score  string
		peerID string
	}
	candidates := make([]candidate, len(pIDs))
	for i, el := range pIDs {
		sum := sha256.Sum256([]byte(msgID + strconv.FormatInt(blockHeight, 10) + el))
		candidates[i] = candidate{
			score:  hex.EncodeToString(sum[:]),
			peerID: el,
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].peerID < candidates[j].peerID
		}
		return candidates[i].score < candidates[j].score
	})
	leaders := make([]string, len(candidates))
	for i := range candidates {
		leaders[i] = candidates[i].peerID
	}
	return leaders, nil
}
