package tss

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	// A maximum-size (128-member) keysign round fits in one envelope. Larger
	// input is split rather than allowing unbounded semantic work in one frame.
	frostVerificationBatchMaxEntries = 128
	frostVerificationBatchInterval   = 100 * time.Millisecond
)

type frostVerificationBatcher struct {
	round           string
	senderPubKey    string
	expectedEntries int
	addedEntries    int
	entries         []FrostVerifyPayload
}

type boundedFrostVerificationPayloads []FrostVerifyPayload

func (entries *boundedFrostVerificationPayloads) UnmarshalJSON(payload []byte) error {
	if bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		*entries = nil
		return nil
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '[' {
		return fmt.Errorf("frost verification batch verifications must be an array")
	}

	// Fragmented windows often contain one echo. Grow with decoded entries;
	// reserving the full party here adds 128 slots to every such frame.
	var decoded []FrostVerifyPayload
	for decoder.More() {
		if len(decoded) == frostVerificationBatchMaxEntries {
			return fmt.Errorf(
				"frost verification batch has more than %d entries, maximum is %d",
				frostVerificationBatchMaxEntries,
				frostVerificationBatchMaxEntries,
			)
		}
		var entry FrostVerifyPayload
		if err = decoder.Decode(&entry); err != nil {
			return err
		}
		decoded = append(decoded, entry)
	}
	token, err = decoder.Token()
	if err != nil {
		return err
	}
	delim, ok = token.(json.Delim)
	if !ok || delim != ']' {
		return fmt.Errorf("frost verification batch verifications must be an array")
	}
	*entries = decoded
	return nil
}

func newFrostVerificationBatcher(round, senderPubKey string, expectedEntries int) *frostVerificationBatcher {
	return &frostVerificationBatcher{
		round:           strings.TrimSpace(round),
		senderPubKey:    strings.TrimSpace(senderPubKey),
		expectedEntries: expectedEntries,
		entries:         make([]FrostVerifyPayload, 0, frostVerificationBatchMaxEntries),
	}
}

func (b *frostVerificationBatcher) add(payload FrostVerifyPayload) error {
	if b == nil {
		return fmt.Errorf("frost verification batch is not initialized")
	}
	if strings.TrimSpace(payload.Round) != b.round {
		return fmt.Errorf("frost verification batch mixes rounds")
	}
	if strings.TrimSpace(payload.SenderPubKey) != b.senderPubKey {
		return fmt.Errorf("frost verification batch mixes senders")
	}
	if len(b.entries) >= frostVerificationBatchMaxEntries {
		return fmt.Errorf("frost verification batch is full")
	}
	b.entries = append(b.entries, payload)
	b.addedEntries++
	return nil
}

func (b *frostVerificationBatcher) shouldFlush() bool {
	return b != nil && (len(b.entries) == frostVerificationBatchMaxEntries ||
		(b.expectedEntries > 0 && b.addedEntries >= b.expectedEntries))
}

func (b *frostVerificationBatcher) reset() {
	if b == nil {
		return
	}
	clear(b.entries)
	b.entries = b.entries[:0]
}

// decodeFrostVerificationBatch accepts only the homogeneous keysign batch wire
// format. The outer FROST frame limit bounds bytes; this bounds entry count and
// requires one authenticated sender and ceremony round per envelope.
func decodeFrostVerificationBatch(payload []byte) ([]FrostVerifyPayload, error) {
	var batch struct {
		Verifications boundedFrostVerificationPayloads `json:"verifications"`
	}
	if err := json.Unmarshal(payload, &batch); err != nil {
		return nil, err
	}
	entries := []FrostVerifyPayload(batch.Verifications)
	if len(entries) == 0 {
		return nil, fmt.Errorf("frost verification batch is empty")
	}

	round := strings.TrimSpace(entries[0].Round)
	senderPubKey := strings.TrimSpace(entries[0].SenderPubKey)
	for i := 1; i < len(entries); i++ {
		if strings.TrimSpace(entries[i].Round) != round {
			return nil, fmt.Errorf("frost verification batch mixes rounds")
		}
		if strings.TrimSpace(entries[i].SenderPubKey) != senderPubKey {
			return nil, fmt.Errorf("frost verification batch mixes senders")
		}
	}
	return entries, nil
}
