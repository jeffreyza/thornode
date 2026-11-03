package observer

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestSendAttestationStateEmpty tests sending an empty attestation state
func TestSendAttestationStateEmpty(t *testing.T) {
	// Disable deadline application during tests
	originalApplyDeadline := p2p.ApplyDeadline.Load()
	p2p.ApplyDeadline.Store(false)
	defer func() { p2p.ApplyDeadline.Store(originalApplyDeadline) }()

	// Create a test instance with empty state
	ag, _, _, _, _, _ := setupTestGossip(t)

	// Create the auto-responder stream
	stream := NewAutoResponderStream("test-peer")

	// Call the method with a timeout
	done := make(chan struct{})
	go func() {
		ag.sendAttestationState(stream)
		close(done)
	}()

	// Wait for completion or timeout
	select {
	case <-done:
		// Method completed
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Method timed out")
	}

	// Get the data written to the stream
	data := stream.GetWrittenData()
	require.NotEmpty(t, data, "Should have written data to the stream")

	// Extract all individual messages from the data
	messages := ParseMessages(data)
	require.NotEmpty(t, messages, "Should have extracted messages")

	// Count message types
	var beginCount, headerCount, dataCount, endCount int
	for _, msg := range messages {
		if len(msg) == 0 {
			continue
		}

		switch {
		case bytes.HasPrefix(msg, prefixBatchBegin):
			beginCount++
		case bytes.HasPrefix(msg, prefixBatchHeader):
			headerCount++
		case bytes.HasPrefix(msg, prefixBatchData):
			dataCount++
		case bytes.Equal(msg, prefixBatchEnd):
			endCount++
		}
	}

	// Verify the basic protocol flow
	assert.Equal(t, 1, beginCount, "Should have one batch begin message")
	assert.Equal(t, 0, headerCount, "Should have one batch header message")
	assert.Equal(t, 0, dataCount, "Should have one batch data message")
	assert.Equal(t, 0, endCount, "Should have one batch end message")

	// For empty state, check the batch header
	for _, msg := range messages {
		if bytes.HasPrefix(msg, prefixBatchBegin) {
			totalBatches := int(binary.LittleEndian.Uint32(msg[1:5]))
			assert.Equal(t, 0, totalBatches, "Empty state should have 1 batch")
		}
	}
}

// TestSendAttestationStateSingleBatch tests sending a small attestation state in a single batch
func TestSendAttestationStateSingleBatch(t *testing.T) {
	// Disable deadline application during tests
	originalApplyDeadline := p2p.ApplyDeadline.Load()
	p2p.ApplyDeadline.Store(false)
	defer func() { p2p.ApplyDeadline.Store(originalApplyDeadline) }()

	// Create a test instance
	ag, _, _, _, _, _ := setupTestGossip(t)

	numVals := 4
	valPrivs := make([]*secp256k1.PrivKey, numVals)
	valPubs := make([]common.PubKey, numVals)
	for i := 0; i < numVals; i++ {
		valPrivs[i] = secp256k1.GenPrivKey()
		bech32Pub, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, valPrivs[i].PubKey())
		require.NoError(t, err, "Should be able to convert pubkey to bech32")
		valPubs[i] = common.PubKey(bech32Pub)
	}

	// Set active validators
	ag.setActiveValidators(valPubs)

	// Create a small set of observed transactions
	numTxs := 3
	for i := 0; i < numTxs; i++ {
		privKey := secp256k1.GenPrivKey()
		tx := &common.Tx{
			ID:    common.TxID(string(rune('A' + i))),
			Chain: common.BSCChain,
		}

		obsTx := &common.ObservedTx{
			Tx: *tx,
		}

		signBz, err := obsTx.GetSignablePayloadWithInbound(false)
		require.NoError(t, err, "Should be able to get signable payload")

		// Create attestation state
		state := ag.observedTxsPool.NewAttestationState(&attestableObservedTx{ObservedTx: obsTx})

		for j := 0; j < numVals; j++ {
			sig, err := valPrivs[j].Sign(signBz)
			require.NoError(t, err, "Should be able to sign payload")

			// Add attestation
			state.attestations = append(state.attestations, attestationSentState{
				attestation: &common.Attestation{
					PubKey:    privKey.PubKey().Bytes(),
					Signature: sig,
				},
				sent: false,
			})
		}

		// Add to attestation gossip
		key := txKey{
			Chain:                  tx.Chain,
			ID:                     tx.ID,
			AllowFutureObservation: false,
			Finalized:              false,
		}

		ag.mu.Lock()
		ag.observedTxs[key] = state
		ag.mu.Unlock()
	}

	// Create the auto-responder stream
	stream := NewAutoResponderStream("test-peer")

	// Call the method with a timeout
	done := make(chan struct{})
	go func() {
		ag.sendAttestationState(stream)
		close(done)
	}()

	// Wait for completion or timeout
	select {
	case <-done:
		// Method completed
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Method timed out")
	}

	// Get the data written to the stream
	data := stream.GetWrittenData()
	require.NotEmpty(t, data, "Should have written data to the stream")

	// Extract all individual messages from the data
	messages := ParseMessages(data)
	require.NotEmpty(t, messages, "Should have extracted messages")

	// Count message types
	var beginCount, headerCount, dataCount, endCount int
	for _, msg := range messages {
		if len(msg) == 0 {
			continue
		}

		switch {
		case bytes.HasPrefix(msg, prefixBatchBegin):
			beginCount++
		case bytes.HasPrefix(msg, prefixBatchHeader):
			headerCount++
		case bytes.HasPrefix(msg, prefixBatchData):
			dataCount++
		case bytes.Equal(msg, prefixBatchEnd):
			endCount++
		}
	}

	// Verify the basic protocol flow
	assert.Equal(t, 1, beginCount, "Should have one batch begin message")
	assert.Equal(t, 1, headerCount, "Should have one batch header message")
	assert.Equal(t, 1, dataCount, "Should have one batch data message")
	assert.Equal(t, 1, endCount, "Should have one batch end message")

	// Check the batch begin message
	for _, msg := range messages {
		if bytes.HasPrefix(msg, prefixBatchBegin) {
			// Extract total batches
			totalBatches := binary.LittleEndian.Uint32(msg[1:5])
			assert.Equal(t, uint32(1), totalBatches, "Should have 1 batch")
		}

		// For data messages, check the batch contents
		if bytes.HasPrefix(msg, prefixBatchData) {
			// Unmarshal the batch
			var batch common.QuorumState
			err := batch.Unmarshal(msg[1:])
			require.NoError(t, err, "Should be able to unmarshal batch data")

			// Verify the batch has the expected number of transactions
			assert.Equal(t, numTxs, len(batch.QuoTxs), "Batch should have all transactions")

			// Verify transaction IDs
			txIDs := make(map[string]bool)
			for _, tx := range batch.QuoTxs {
				txIDs[string(tx.ObsTx.Tx.ID)] = true
			}

			for j := 0; j < numTxs; j++ {
				assert.True(t, txIDs[string(rune('A'+j))], "Should contain transaction ID %c", 'A'+j)
			}
		}
	}
}

// TestSendAttestationStateMultipleBatches tests sending a large attestation state in multiple batches
func TestSendAttestationStateMultipleBatches(t *testing.T) {
	// Disable deadline application during tests
	originalApplyDeadline := p2p.ApplyDeadline.Load()
	p2p.ApplyDeadline.Store(false)
	defer func() { p2p.ApplyDeadline.Store(originalApplyDeadline) }()

	// Create a test instance
	ag, _, _, _, _, _ := setupTestGossip(t)

	// Set a small byte budget so the state splits into multiple batches
	origMaxAttestationBatchBytes := maxAttestationBatchBytes
	maxAttestationBatchBytes = 1000                                            // Small value for testing
	defer func() { maxAttestationBatchBytes = origMaxAttestationBatchBytes }() // Restore original value

	// Create a set of observed transactions large enough to span several batches
	numTxs := 5

	numVals := 4
	valPrivs := make([]*secp256k1.PrivKey, numVals)
	valPubs := make([]common.PubKey, numVals)
	for i := 0; i < numVals; i++ {
		valPrivs[i] = secp256k1.GenPrivKey()
		bech32Pub, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, valPrivs[i].PubKey())
		require.NoError(t, err, "Should be able to convert pubkey to bech32")
		valPubs[i] = common.PubKey(bech32Pub)
	}

	// Set active validators
	ag.setActiveValidators(valPubs)

	for i := 0; i < numTxs; i++ {
		tx := &common.Tx{
			ID:    common.TxID(string(rune('A' + i))),
			Chain: common.BSCChain,
		}

		obsTx := &common.ObservedTx{
			Tx: *tx,
		}

		// Create attestation state
		state := ag.observedTxsPool.NewAttestationState(&attestableObservedTx{ObservedTx: obsTx})

		signBz, err := obsTx.GetSignablePayloadWithInbound(false)
		require.NoError(t, err, "Should be able to get signable payload")

		// Create attestations
		for j := 0; j < numVals; j++ {
			sig, err := valPrivs[j].Sign(signBz)
			require.NoError(t, err, "Should be able to sign payload")

			// Add attestation
			state.attestations = append(state.attestations, attestationSentState{
				attestation: &common.Attestation{
					PubKey:    valPrivs[j].PubKey().Bytes(),
					Signature: sig,
				},
				sent: false,
			})
		}

		// Add to attestation gossip
		key := txKey{
			Chain:                  tx.Chain,
			ID:                     tx.ID,
			AllowFutureObservation: false,
			Finalized:              false,
		}

		ag.mu.Lock()
		ag.observedTxs[key] = state
		ag.mu.Unlock()
	}

	// Create the auto-responder stream
	stream := NewAutoResponderStream("test-peer")

	// Call the method with a timeout
	done := make(chan struct{})
	go func() {
		ag.sendAttestationState(stream)
		close(done)
	}()

	// Wait for completion or timeout
	select {
	case <-done:
		// Method completed
	case <-time.After(1 * time.Second):
		t.Fatal("Method timed out")
	}

	// Get the data written to the stream
	data := stream.GetWrittenData()
	require.NotEmpty(t, data, "Should have written data to the stream")

	// Extract all individual messages from the data
	messages := ParseMessages(data)
	require.NotEmpty(t, messages, "Should have extracted messages")

	// Count message types
	var beginCount, headerCount, dataCount, endCount int
	for _, msg := range messages {
		if len(msg) == 0 {
			continue
		}

		switch {
		case bytes.HasPrefix(msg, prefixBatchBegin):
			beginCount++
		case bytes.HasPrefix(msg, prefixBatchHeader):
			headerCount++
		case bytes.HasPrefix(msg, prefixBatchData):
			dataCount++
		case bytes.Equal(msg, prefixBatchEnd):
			endCount++
		}
	}

	// Verify the basic protocol flow
	assert.Equal(t, 1, beginCount, "Should have one batch begin message")
	assert.GreaterOrEqual(t, headerCount, 2, "Byte budget should force multiple batches")
	assert.Equal(t, headerCount, dataCount, "Every batch header should have a data message")
	assert.Equal(t, 1, endCount, "Should have one batch end message")

	// Check the batch begin message
	for i, msg := range messages {
		if bytes.HasPrefix(msg, prefixBatchBegin) {
			// Extract total batches
			totalBatches := binary.LittleEndian.Uint32(msg[1:5])
			assert.Equal(t, uint32(headerCount), totalBatches, "Announced batch count should match batches sent")
		}

		// For data messages, check the batch number and contents
		if bytes.HasPrefix(msg, prefixBatchHeader) && len(msg) >= 9 {
			// Extract batch number
			batchNum := binary.LittleEndian.Uint32(msg[1:5])
			assert.Less(t, batchNum, uint32(headerCount), "Batch number should be valid")

			// Get the batch data (should be the next message)
			if len(messages) > i+1 {
				batchData := messages[i+1]

				assert.True(t, bytes.HasPrefix(batchData, prefixBatchData))

				// Every frame must respect the byte budget (minus the one-byte prefix)
				assert.LessOrEqual(t, len(batchData)-1, maxAttestationBatchBytes,
					"Batch %d should fit the byte budget", batchNum)

				// Unmarshal the batch
				var batch common.QuorumState
				err := batch.Unmarshal(batchData[1:])
				if err != nil {
					t.Logf("Error unmarshaling batch %d (likely not a batch): %v", batchNum, err)
					continue
				}

				assert.NotEmpty(t, batch.QuoTxs, "Batch %d should carry at least one transaction", batchNum)
			}
		}
	}

	// Collect all transactions from all batches to verify we have them all
	allTxs := make(map[string]bool)
	for _, msg := range messages {
		if bytes.HasPrefix(msg, prefixBatchData) {
			// Try to unmarshal the next message as batch data
			var batch common.QuorumState
			if err := batch.Unmarshal(msg[1:]); err == nil {
				// Record all transaction IDs
				for _, tx := range batch.QuoTxs {
					allTxs[string(tx.ObsTx.Tx.ID)] = true
				}
			}
		}
	}

	// Verify we have all expected transactions
	assert.Equal(t, numTxs, len(allTxs), "Should have all transactions across all batches")
	for i := 0; i < numTxs; i++ {
		assert.True(t, allTxs[string(rune('A'+i))], "Should contain transaction ID %c", 'A'+i)
	}
}

// TestReceiveBatchedAttestationState tests receiving batched attestation state
func TestReceiveBatchedAttestationState(t *testing.T) {
	// Disable deadline application during tests
	originalApplyDeadline := p2p.ApplyDeadline.Load()
	p2p.ApplyDeadline.Store(false)
	defer func() { p2p.ApplyDeadline.Store(originalApplyDeadline) }()

	// Create a test instance
	ag, _, _, _, _, _ := setupTestGossip(t)

	// The receiver accepts whatever batch boundaries the sender chose; this
	// test simulates a sender that packed two txs per batch.
	batchSize := 2

	numVals := 4

	// Create test data: 5 transactions split into 3 batches
	numTxs := 5
	expectedBatches := (numTxs + batchSize - 1) / batchSize

	numSolvencies := 3

	valPrivs := make([]*secp256k1.PrivKey, numVals)
	valPubs := make([]common.PubKey, numVals)
	for i := 0; i < numVals; i++ {
		valPrivs[i] = secp256k1.GenPrivKey()
		bech32Pub, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, valPrivs[i].PubKey())
		require.NoError(t, err, "Should be able to convert pubkey to bech32")
		valPubs[i] = common.PubKey(bech32Pub)
	}

	ag.setActiveValidators(valPubs)

	// Create the test transactions
	txs := make([]*common.QuorumTx, numTxs)
	for i := range numTxs {
		// Create observed tx
		obsTx := common.ObservedTx{
			Tx: common.Tx{
				ID:    common.TxID(string(rune('A' + i))),
				Chain: common.BSCChain,
			},
		}

		inbound := i%2 == 0 // Alternate inbound/outbound
		signBz, err := obsTx.GetSignablePayloadWithInbound(inbound)
		require.NoError(t, err, "Should be able to get signable payload")

		atts := make([]*common.Attestation, numVals)

		for j := range numVals {
			sig, err := valPrivs[j].Sign(signBz)
			require.NoError(t, err, "Should be able to sign payload")
			atts[j] = &common.Attestation{
				PubKey:    valPrivs[j].PubKey().Bytes(),
				Signature: sig,
			}
		}

		// Create quorum tx
		txs[i] = &common.QuorumTx{
			ObsTx:        obsTx,
			Inbound:      inbound,
			Attestations: atts,
		}
	}

	solvencies := make([]*common.QuorumSolvency, numSolvencies)
	for i := range numSolvencies {
		s := &common.Solvency{
			Chain:  common.BSCChain,
			Height: int64(i),
			Coins: []common.Coin{
				{
					Asset:  common.BNBBEP20Asset,
					Amount: cosmos.NewUint(100 + uint64(i)),
				},
			},
		}

		signBz, err := s.GetSignablePayload()
		require.NoError(t, err, "Should be able to get signable payload")

		atts := make([]*common.Attestation, numVals)
		for j := range numVals {
			sig, err := valPrivs[j].Sign(signBz)
			require.NoError(t, err, "Should be able to sign payload")
			atts[j] = &common.Attestation{
				PubKey:    valPrivs[j].PubKey().Bytes(),
				Signature: sig,
			}
		}

		solvencies[i] = &common.QuorumSolvency{
			Solvency:     s,
			Attestations: atts,
		}
	}

	// Create a simulated peer to send these transactions
	// Create stream pair for testing
	clientReader := bytes.NewBuffer(nil)
	clientWriter := &bytes.Buffer{}

	serverReader := clientWriter
	serverWriter := clientReader

	var sharedMu sync.Mutex

	peerStream := &MockStream{
		reader: serverReader,
		writer: serverWriter,
		peer:   "test-peer",
		mu:     &sharedMu,
	}

	ourStream := &MockStream{
		reader: clientReader,
		writer: clientWriter,
		peer:   ag.host.ID(),
		mu:     &sharedMu,
	}

	var wg sync.WaitGroup
	wg.Add(1)

	// Start a goroutine to simulate sending batched state from the peer
	go func() {
		defer wg.Done()
		// Wait for acknowledgment
		ack, err := p2p.ReadStreamWithBuffer(ourStream)
		require.NoError(t, err, "Should read begin acknowledgment")
		require.NotEmpty(t, ack, "Should receive acknowledgment")
		require.Equal(t, "ack_begin", string(ack), "Should receive 'ack_begin' after batch begin")

		t.Log("Received batch begin acknowledgment")

		// Send each batch
		for batchIdx := 0; batchIdx < expectedBatches; batchIdx++ {
			startIdx := batchIdx * batchSize
			endIdx := startIdx + batchSize

			if endIdx > numTxs {
				endIdx = numTxs
			}

			batchTxs := txs[startIdx:endIdx]

			var batchSolvencies []*common.QuorumSolvency
			if startIdx < numSolvencies {
				endIdxSolvency := startIdx + batchSize
				if endIdxSolvency > numSolvencies {
					endIdxSolvency = numSolvencies
				}

				batchSolvencies = solvencies[startIdx:endIdxSolvency]
			}

			// Create batch
			batch := common.QuorumState{
				QuoTxs:        batchTxs,
				QuoSolvencies: batchSolvencies,
			}

			// Marshal batch
			batchData, _ := batch.Marshal()

			// Create batch header
			batchHeader := make([]byte, 9)
			copy(batchHeader, prefixBatchHeader)
			binary.LittleEndian.PutUint32(batchHeader[1:5], uint32(batchIdx))
			binary.LittleEndian.PutUint32(batchHeader[5:9], uint32(len(batchData)))

			err = p2p.WriteStreamWithBuffer(batchHeader, ourStream)
			require.NoError(t, err, "Should write batch header")

			t.Log("Sent batch header")

			ack, err = p2p.ReadStreamWithBuffer(ourStream)
			require.NoError(t, err, "Should read header acknowledgment")
			require.Equal(t, "ack_header", string(ack), "Should receive 'ack_header' after batch header")

			msgData := make([]byte, len(batchData)+1)
			copy(msgData, prefixBatchData)
			copy(msgData[1:], batchData)

			err = p2p.WriteStreamWithBuffer(msgData, ourStream)
			require.NoError(t, err, "Should write batch data")

			t.Log("Sent batch data")

			// Wait for acknowledgment
			ack, err = p2p.ReadStreamWithBuffer(ourStream)
			require.NoError(t, err, "Should read data acknowledgment")
			require.Equal(t, "ack_data", string(ack), "Should receive 'ack_data' after batch data")
		}

		// Send batch end
		err = p2p.WriteStreamWithBuffer(prefixBatchEnd, ourStream)
		require.NoError(t, err, "Should write batch end")

		t.Log("Sent batch end")

		// Wait for acknowledgment
		ack, err = p2p.ReadStreamWithBuffer(ourStream)
		require.NoError(t, err, "Should read done acknowledgment")
		require.Equal(t, "done", string(ack), "Should receive 'done' after batch end")
	}()

	// Call receiveBatchedAttestationState with the initial batch begin message
	beginHeader := make([]byte, 4)
	binary.LittleEndian.PutUint32(beginHeader, uint32(expectedBatches))

	err := ag.receiveBatchedAttestationState(peerStream, beginHeader)
	require.NoError(t, err)

	// Wait for the sending goroutine to finish
	wg.Wait()

	// Verify that all transactions were added to the state
	ag.mu.Lock()
	defer ag.mu.Unlock()

	// Check for all test transactions by ID
	txCount := 0
	attCount := 0
	for k, ots := range ag.observedTxs {
		if k.Chain == common.BSCChain && len(k.ID) == 1 && k.ID[0] >= 'A' && k.ID[0] < 'A'+byte(numTxs) {
			txCount++
			ots.mu.Lock()
			for _, att := range ots.attestations {
				if att.attestation != nil && att.attestation.PubKey != nil {
					attCount++
				}
			}
			ots.mu.Unlock()
		}
	}

	assert.Equal(t, numTxs, txCount, "All transactions should be added to the state")
	assert.Equal(t, numTxs*numVals, attCount, "All attestations should be added to the state")

	solvencyCount := 0
	solvencyAttCount := 0
	for _, s := range ag.solvencies {
		if s.Item.Chain == common.BSCChain && s.Item.Height+100 == int64(s.Item.Coins[0].Amount.Uint64()) {
			solvencyCount++
			s.mu.Lock()
			for _, att := range s.attestations {
				if att.attestation != nil && att.attestation.PubKey != nil {
					solvencyAttCount++
				}
			}
			s.mu.Unlock()
		}
	}

	assert.Equal(t, numSolvencies, solvencyCount, "All solvencies should be added to the state")
	assert.Equal(t, numSolvencies*numVals, solvencyAttCount, "All solvency attestations should be added to the state")
}

func TestSendReceiveBatchedAttestationState(t *testing.T) {
	// Create a test instance
	agSender, _, _, _, _, _ := setupTestGossip(t)
	agReceiver, _, _, _, _, _ := setupTestGossip(t)

	// Set a small byte budget so the state splits into multiple batches
	origMaxAttestationBatchBytes := maxAttestationBatchBytes
	maxAttestationBatchBytes = 1000                                            // Small value for testing
	defer func() { maxAttestationBatchBytes = origMaxAttestationBatchBytes }() // Restore original value

	// Create enough items that the byte budget forces several batches
	numTxs := 5
	numSolvencies := 3
	numVals := 4
	valPrivs := make([]*secp256k1.PrivKey, numVals)
	valPubs := make([]common.PubKey, numVals)
	for i := 0; i < numVals; i++ {
		valPrivs[i] = secp256k1.GenPrivKey()
		bech32Pub, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, valPrivs[i].PubKey())
		require.NoError(t, err, "Should be able to convert pubkey to bech32")
		valPubs[i] = common.PubKey(bech32Pub)
	}

	// Set active validators
	agSender.setActiveValidators(valPubs)
	agReceiver.setActiveValidators(valPubs)

	// Set attestation state on sender
	for i := 0; i < numTxs; i++ {
		tx := &common.Tx{
			ID:    common.TxID(string(rune('A' + i))),
			Chain: common.BSCChain,
		}

		obsTx := &common.ObservedTx{
			Tx: *tx,
		}

		// Create attestation state
		state := agSender.observedTxsPool.NewAttestationState(&attestableObservedTx{ObservedTx: obsTx})

		signBz, err := obsTx.GetSignablePayloadWithInbound(false)
		require.NoError(t, err, "Should be able to get signable payload")

		// Create attestations
		for j := 0; j < numVals; j++ {
			sig, err := valPrivs[j].Sign(signBz)
			require.NoError(t, err, "Should be able to sign payload")

			// Add attestation
			state.attestations = append(state.attestations, attestationSentState{
				attestation: &common.Attestation{
					PubKey:    valPrivs[j].PubKey().Bytes(),
					Signature: sig,
				},
				sent: false,
			})
		}

		// Add to attestation gossip
		key := txKey{
			Chain:                  tx.Chain,
			ID:                     tx.ID,
			AllowFutureObservation: false,
			Finalized:              false,
		}

		agSender.mu.Lock()
		agSender.observedTxs[key] = state
		agSender.mu.Unlock()
	}

	for i := range numSolvencies {
		s := &common.Solvency{
			Chain:  common.BSCChain,
			Height: int64(i),
			Coins: []common.Coin{
				{
					Asset:  common.BNBBEP20Asset,
					Amount: cosmos.NewUint(100 + uint64(i)),
				},
			},
		}

		signBz, err := s.GetSignablePayload()
		require.NoError(t, err, "Should be able to get signable payload")

		state := agSender.solvenciesPool.NewAttestationState(s)

		for j := range numVals {
			var sig []byte
			sig, err = valPrivs[j].Sign(signBz)
			require.NoError(t, err, "Should be able to sign payload")

			state.attestations = append(state.attestations, attestationSentState{
				attestation: &common.Attestation{
					PubKey:    valPrivs[j].PubKey().Bytes(),
					Signature: sig,
				},
			})
		}

		k, err := s.Hash()
		require.NoError(t, err, "Should be able to hash solvency")

		agSender.mu.Lock()
		agSender.solvencies[k] = state
		agSender.mu.Unlock()
	}

	// Create a simulated peer to send these transactions
	// Create stream pair for testing
	senderReader := bytes.NewBuffer(nil)
	senderWriter := &bytes.Buffer{}

	receiverReader := senderWriter
	receiverWriter := senderReader

	var sharedMu sync.Mutex

	receiverStream := &MockStream{
		reader: receiverReader,
		writer: receiverWriter,
		peer:   agSender.host.ID(),
		mu:     &sharedMu,
	}

	senderStream := &MockStream{
		reader: senderReader,
		writer: senderWriter,
		peer:   agReceiver.host.ID(),
		mu:     &sharedMu,
	}

	// Call the method with a timeout
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		agSender.handleStreamAttestationState(senderStream)
	}()

	go func() {
		defer wg.Done()
		agReceiver.askStreamForState(receiverStream)
	}()

	// Wait for both goroutines to finish
	wg.Wait()

	// agReceiver should have received all transactions
	agReceiver.mu.Lock()
	defer agReceiver.mu.Unlock()
	// Check for all test transactions by ID
	txCount := 0
	attCount := 0
	for k, ots := range agReceiver.observedTxs {
		if k.Chain == common.BSCChain && len(k.ID) == 1 && k.ID[0] >= 'A' && k.ID[0] < 'A'+byte(numTxs) {
			txCount++
			ots.mu.Lock()
			for _, att := range ots.attestations {
				if att.attestation != nil && att.attestation.PubKey != nil {
					attCount++
				}
			}
			ots.mu.Unlock()
		}
	}
	assert.Equal(t, numTxs, txCount, "All transactions should be added to the state")
	assert.Equal(t, numTxs*numVals, attCount, "All attestations should be added to the state")

	solvencyCount := 0
	solvencyAttCount := 0
	for _, s := range agReceiver.solvencies {
		if s.Item.Chain == common.BSCChain && s.Item.Height+100 == int64(s.Item.Coins[0].Amount.Uint64()) {
			solvencyCount++
			s.mu.Lock()
			for _, att := range s.attestations {
				if att.attestation != nil && att.attestation.PubKey != nil {
					solvencyAttCount++
				}
			}
			s.mu.Unlock()
		}
	}

	assert.Equal(t, numSolvencies, solvencyCount, "All solvencies should be added to the state")
	assert.Equal(t, numSolvencies*numVals, solvencyAttCount, "All solvency attestations should be added to the state")
}

// AutoResponderStream is a stream that automatically responds to protocol messages
type AutoResponderStream struct {
	pendingRead []byte
	MockStream
	writtenData *bytes.Buffer
	readData    chan []byte
	callCount   int
}

// NewAutoResponderStream creates a MockStream that automatically responds to protocol messages
func NewAutoResponderStream(peer peer.ID) *AutoResponderStream {
	readChan := make(chan []byte, 10)
	writtenData := &bytes.Buffer{}

	return &AutoResponderStream{
		MockStream: MockStream{
			peer: peer,
			mu:   new(sync.Mutex),
		},
		writtenData: writtenData,
		readData:    readChan,
		callCount:   0,
	}
}

// Read implementation that returns the next item from the readData channel
func (s *AutoResponderStream) Read(p []byte) (n int, err error) {
	if len(s.pendingRead) == 0 {
		select {
		case s.pendingRead = <-s.readData:
		case <-time.After(100 * time.Millisecond):
			return 0, io.EOF
		}
	}
	n = copy(p, s.pendingRead)
	s.pendingRead = s.pendingRead[n:]
	return n, nil
}

// Write implementation that captures the data and automatically responds
func (s *AutoResponderStream) Write(p []byte) (n int, err error) {
	// Count the call
	s.callCount++

	// Capture the data
	n, err = s.writtenData.Write(p)
	if err != nil {
		return n, err
	}

	// Check if this is a complete message
	if len(p) < p2p.LengthHeader {
		return n, nil
	}

	// Extract the length
	length := binary.LittleEndian.Uint32(p[:p2p.LengthHeader])
	if len(p) < p2p.LengthHeader+int(length) {
		return n, nil
	}

	// Extract the message
	message := p[p2p.LengthHeader : p2p.LengthHeader+int(length)]

	respHeader := make([]byte, p2p.LengthHeader)
	// If we got a batch end message, respond with "done"

	var respData []byte
	switch {
	case bytes.HasPrefix(message, prefixBatchBegin):
		respData = []byte("ack_begin")
	case bytes.HasPrefix(message, prefixBatchHeader):
		respData = []byte("ack_header")
	case bytes.HasPrefix(message, prefixBatchData):
		respData = []byte("ack_data")
	case bytes.HasPrefix(message, prefixBatchEnd):
		respData = []byte("done")
	default:
		panic(fmt.Errorf("unexpected message: %s", hex.EncodeToString(message)))
	}

	binary.LittleEndian.PutUint32(respHeader, uint32(len(respData)))

	// Queue the response
	s.readData <- append(respHeader, respData...)

	return n, nil
}

// GetWrittenData returns all data written to the stream
func (s *AutoResponderStream) GetWrittenData() []byte {
	return s.writtenData.Bytes()
}

// ParseMessages extracts all length-prefixed messages from raw data
func ParseMessages(data []byte) [][]byte {
	var messages [][]byte
	buf := bytes.NewBuffer(data)

	for buf.Len() >= p2p.LengthHeader {
		// Read length header
		lengthBytes := make([]byte, p2p.LengthHeader)
		n, err := buf.Read(lengthBytes)
		if err != nil || n != p2p.LengthHeader {
			break
		}

		length := binary.LittleEndian.Uint32(lengthBytes)
		if buf.Len() < int(length) {
			break
		}

		message := make([]byte, length)
		n, err = buf.Read(message)
		if err != nil || n != int(length) {
			break
		}

		messages = append(messages, message)
	}

	return messages
}

// TestSendAttestationStateEmptyProtocolCorrect is a regression test for the
// totalBatches==0 fallthrough bug in sendAttestationState.
//
// The existing TestSendAttestationStateEmpty uses AutoResponderStream which
// always responds "ack_begin" to prefixBatchBegin regardless of totalBatches.
// That hides the bug because the buggy code falls through and (eventually)
// fails on a different check.
//
// In the real wire protocol, when the sender writes prefixBatchBegin with
// totalBatches=0, the receiver replies with StreamMsgDone (see
// receiveBatchedAttestationState). The pre-fix sender validated the "done"
// ack but then fell through to the streamAckBegin check and logged a false
// "unexpected response to batch begin" error. This test uses a stream that
// responds protocol-correctly with "done" and verifies the sender exits
// cleanly without sending additional batch messages and without the false
// error.
func TestSendAttestationStateEmptyProtocolCorrect(t *testing.T) {
	originalApplyDeadline := p2p.ApplyDeadline.Load()
	p2p.ApplyDeadline.Store(false)
	defer func() { p2p.ApplyDeadline.Store(originalApplyDeadline) }()

	ag, _, _, _, _, _ := setupTestGossip(t)

	stream := newDoneResponderStream("test-peer")

	done := make(chan struct{})
	go func() {
		ag.sendAttestationState(stream)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("sendAttestationState did not return after StreamMsgDone ack")
	}

	// Sender must have written exactly one prefixBatchBegin and nothing else
	// (no header, no data, no end) - the empty-state path completes after the
	// done ack.
	messages := ParseMessages(stream.GetWrittenData())
	require.NotEmpty(t, messages, "Should have written at least one message")

	var beginCount, headerCount, dataCount, endCount int
	for _, msg := range messages {
		if len(msg) == 0 {
			continue
		}
		switch {
		case bytes.HasPrefix(msg, prefixBatchBegin):
			beginCount++
			totalBatches := int(binary.LittleEndian.Uint32(msg[1:5]))
			assert.Equal(t, 0, totalBatches, "Empty state should encode totalBatches=0 on the wire")
		case bytes.HasPrefix(msg, prefixBatchHeader):
			headerCount++
		case bytes.HasPrefix(msg, prefixBatchData):
			dataCount++
		case bytes.Equal(msg, prefixBatchEnd):
			endCount++
		}
	}
	assert.Equal(t, 1, beginCount, "Should write exactly one batch begin")
	assert.Equal(t, 0, headerCount, "Should not write any batch header for empty state")
	assert.Equal(t, 0, dataCount, "Should not write any batch data for empty state")
	assert.Equal(t, 0, endCount, "Should not write batch end after a done ack")
}

// doneResponderStream is a MockStream that responds protocol-correctly to a
// prefixBatchBegin with totalBatches=0 by replying with StreamMsgDone, instead
// of always replying ack_begin like AutoResponderStream. Used to verify the
// sender does not fall through to the ack_begin check after a valid done ack.
type doneResponderStream struct {
	pendingRead []byte
	MockStream
	writtenData *bytes.Buffer
	readData    chan []byte
}

func newDoneResponderStream(peer peer.ID) *doneResponderStream {
	return &doneResponderStream{
		MockStream: MockStream{
			peer: peer,
			mu:   new(sync.Mutex),
		},
		writtenData: &bytes.Buffer{},
		readData:    make(chan []byte, 4),
	}
}

func (s *doneResponderStream) Read(p []byte) (n int, err error) {
	if len(s.pendingRead) == 0 {
		select {
		case s.pendingRead = <-s.readData:
		case <-time.After(100 * time.Millisecond):
			return 0, io.EOF
		}
	}
	n = copy(p, s.pendingRead)
	s.pendingRead = s.pendingRead[n:]
	return n, nil
}

func (s *doneResponderStream) Write(p []byte) (n int, err error) {
	n, err = s.writtenData.Write(p)
	if err != nil {
		return n, err
	}
	if len(p) < p2p.LengthHeader {
		return n, nil
	}
	length := binary.LittleEndian.Uint32(p[:p2p.LengthHeader])
	if len(p) < p2p.LengthHeader+int(length) {
		return n, nil
	}
	message := p[p2p.LengthHeader : p2p.LengthHeader+int(length)]

	// Only respond to prefixBatchBegin: reply with the protocol-correct
	// StreamMsgDone if totalBatches==0, ack_begin otherwise.
	if !bytes.HasPrefix(message, prefixBatchBegin) {
		return n, nil
	}
	var resp []byte
	totalBatches := uint32(0)
	if len(message) >= 5 {
		totalBatches = binary.LittleEndian.Uint32(message[1:5])
	}
	if totalBatches == 0 {
		resp = []byte(p2p.StreamMsgDone)
	} else {
		resp = []byte("ack_begin")
	}
	respHeader := make([]byte, p2p.LengthHeader)
	binary.LittleEndian.PutUint32(respHeader, uint32(len(resp)))
	s.readData <- append(respHeader, resp...)
	return n, nil
}

func (s *doneResponderStream) GetWrittenData() []byte {
	return s.writtenData.Bytes()
}

// TestAskStreamForStateEmptyState is a regression test for the prefix-strip
// bug in askStreamForState.
//
// Before the fix, askStreamForState passed the full initialData (including
// the prefixBatchBegin byte) to receiveBatchedAttestationState, which then
// read initialData[:4] as totalBatches and got garbage [0x02, b0, b1, b2]
// instead of the real [b0, b1, b2, b3]. The bug was invisible whenever the
// real totalBatches was non-zero (the receiver loop terminates on
// prefixBatchEnd, not on a counter), so existing send/receive tests with
// non-empty state didn't catch it. It only manifested in the empty-state
// case: real totalBatches=0 was misread as 2, the receiver sent ack_begin
// instead of done, and the sender errored.
//
// This test exercises the full ask/handle wire flow with an EMPTY sender
// state and verifies both sides complete cleanly.
func TestAskStreamForStateEmptyState(t *testing.T) {
	originalApplyDeadline := p2p.ApplyDeadline.Load()
	p2p.ApplyDeadline.Store(false)
	defer func() { p2p.ApplyDeadline.Store(originalApplyDeadline) }()

	agSender, _, _, _, _, _ := setupTestGossip(t)
	agReceiver, _, _, _, _, _ := setupTestGossip(t)

	// Authorize the receiver to ask agSender for state.
	agSender.stateInitMu.Lock()
	agSender.stateInitPeers = map[peer.ID]bool{agReceiver.host.ID(): true}
	agSender.stateInitMu.Unlock()

	// Buffered stream pair: what one writes the other reads.
	senderReader := bytes.NewBuffer(nil)
	senderWriter := &bytes.Buffer{}
	receiverReader := senderWriter
	receiverWriter := senderReader

	var sharedMu sync.Mutex
	receiverStream := &MockStream{
		reader: receiverReader,
		writer: receiverWriter,
		peer:   agSender.host.ID(),
		mu:     &sharedMu,
	}
	senderStream := &MockStream{
		reader: senderReader,
		writer: senderWriter,
		peer:   agReceiver.host.ID(),
		mu:     &sharedMu,
	}

	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			agSender.handleStreamAttestationState(senderStream)
		}()
		go func() {
			defer wg.Done()
			agReceiver.askStreamForState(receiverStream)
		}()
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ask/handle did not complete on empty state - totalBatches=0 wire path is wedged")
	}

	// Receiver should not have populated any observed txs (sender was empty).
	agReceiver.mu.Lock()
	defer agReceiver.mu.Unlock()
	assert.Equal(t, 0, len(agReceiver.observedTxs), "receiver should have no txs after empty-state init")
	assert.Equal(t, 0, len(agReceiver.networkFees), "receiver should have no network fees after empty-state init")
	assert.Equal(t, 0, len(agReceiver.solvencies), "receiver should have no solvencies after empty-state init")
}

// stateSyncTestFrameTimeout replaces the ordinary per-frame read deadline in
// the deadline tests; their stalls, slow batches and budgets are multiples of
// it. It also bounds how long the sender waits for the receiver to process
// each batch, which includes leveldb writes. 250ms was too short on a loaded
// CI runner: TestStateSyncExportReleaseOnEveryExit failed its retry step in
// 1 of 70 to 1 of 280 subtests with 8 copies in parallel; 1s gave 0 of 1,120.
const stateSyncTestFrameTimeout = time.Second

// Successful stream cleanup may log after the handler and its test return.
type stateSyncLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *stateSyncLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *stateSyncLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

type stateSyncReadDeadline struct {
	at       time.Time
	deadline time.Time
	ordinary bool
}

// TCP buffers the next small header while the receiver processes a batch.
// Unlike the older pipe fixture, these streams enforce deadlines. Scale only
// the sender's ordinary 40-second reads to keep regressions fast; record the
// requested deadline and pass absolute exchange deadlines through unchanged.
// Receiver reads and all writes retain their production deadlines.
type stateSyncDeadlineStream struct {
	MockStream
	transport  net.Conn
	shortReads bool
	reads      chan stateSyncReadDeadline
	resets     atomic.Int32
	closes     atomic.Int32
}

func (s *stateSyncDeadlineStream) Read(p []byte) (int, error) {
	return s.transport.Read(p)
}

func (s *stateSyncDeadlineStream) Write(p []byte) (int, error) {
	return s.transport.Write(p)
}

func (s *stateSyncDeadlineStream) SetReadDeadline(deadline time.Time) error {
	now := time.Now()
	remaining := deadline.Sub(now)
	ordinary := remaining > p2p.TimeoutReadPayload-time.Second && remaining <= p2p.TimeoutReadPayload
	s.reads <- stateSyncReadDeadline{at: now, deadline: deadline, ordinary: ordinary}
	if ordinary && s.shortReads {
		deadline = now.Add(stateSyncTestFrameTimeout)
	}
	return s.transport.SetReadDeadline(deadline)
}

func (s *stateSyncDeadlineStream) SetWriteDeadline(deadline time.Time) error {
	return s.transport.SetWriteDeadline(deadline)
}

func (s *stateSyncDeadlineStream) Reset() error {
	s.resets.Add(1)
	return s.transport.Close()
}

func (s *stateSyncDeadlineStream) Close() error {
	s.closes.Add(1)
	return s.transport.Close()
}

func stateSyncDeadlinePair(t *testing.T) (*stateSyncDeadlineStream, *stateSyncDeadlineStream) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	client, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	server, err := listener.Accept()
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	require.NoError(t, listener.Close())
	wrap := func(conn net.Conn, remote peer.ID, shortReads bool) *stateSyncDeadlineStream {
		return &stateSyncDeadlineStream{
			MockStream: MockStream{peer: remote},
			transport:  conn,
			shortReads: shortReads,
			reads:      make(chan stateSyncReadDeadline, 32),
		}
	}
	return wrap(server, "receiver", true), wrap(client, "sender", false)
}

func stateSyncDeadlineFixture(t *testing.T, count int) *AttestationGossip {
	t.Helper()
	sender, _, _, _, _, _ := setupTestGossip(t)
	sender.logger = zerolog.Nop()
	sender.config.StateSyncTimeout = 15 * time.Second
	for i := range count {
		fee := &common.NetworkFee{Chain: common.BTCChain, Height: int64(123 + i), TransactionSize: 100, TransactionRate: 42}
		payload, err := fee.GetSignablePayload()
		require.NoError(t, err)
		signature, err := sender.privKey.Sign(payload)
		require.NoError(t, err)
		att := &common.Attestation{PubKey: sender.pubKey, Signature: signature}
		sender.networkFees[*fee] = &AttestationState[*common.NetworkFee]{
			Item: fee, attestations: []attestationSentState{{attestation: att}},
		}
	}
	return sender
}

func stateSyncDeadlineTestSettings(t *testing.T) {
	t.Helper()
	previousLimit, previousDeadline := maxAttestationBatchBytes, p2p.ApplyDeadline.Load()
	maxAttestationBatchBytes = 1 // One small, valid item per batch.
	p2p.ApplyDeadline.Store(true)
	t.Cleanup(func() {
		maxAttestationBatchBytes = previousLimit
		p2p.ApplyDeadline.Store(previousDeadline)
	})
}

func waitStateSyncDeadlineHandler(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("state handler did not finish")
	}
}

func stateSyncNextReadDeadline(t *testing.T, stream *stateSyncDeadlineStream) stateSyncReadDeadline {
	t.Helper()
	select {
	case deadline := <-stream.reads:
		return deadline
	case <-time.After(5 * time.Second):
		t.Fatal("sender did not start the expected read")
		return stateSyncReadDeadline{}
	}
}

// This receiver keeps the existing wire messages but processes each batch
// inline, matching the bounded receiver's ACK order. Processing uses the real
// import handler; only the injected processing delay is synthetic.
func stateSyncReceiveInline(t *testing.T, receiver *AttestationGossip, client *stateSyncDeadlineStream, slowBatch int) {
	t.Helper()
	write := func(payload []byte) { require.NoError(t, p2p.WriteStreamWithBuffer(payload, client)) }
	read := func(prefix []byte) []byte {
		message, err := p2p.ReadStreamWithBuffer(client)
		require.NoError(t, err)
		require.True(t, bytes.HasPrefix(message, prefix))
		return message
	}
	write(prefixSendState)
	begin := read(prefixBatchBegin)
	require.Len(t, begin, 5)
	count := binary.LittleEndian.Uint32(begin[1:])
	require.EqualValues(t, 3, count)
	write([]byte(streamAckBegin))
	for i := range count {
		header := read(prefixBatchHeader)
		require.Len(t, header, 9)
		require.Equal(t, i, binary.LittleEndian.Uint32(header[1:5]))
		write([]byte(streamAckHeader))
		data := read(prefixBatchData)
		require.EqualValues(t, len(data)-1, binary.LittleEndian.Uint32(header[5:9]))
		var batch common.QuorumState
		require.NoError(t, batch.Unmarshal(data[1:]))
		write([]byte(streamAckData))
		if int(i)+1 == slowBatch {
			time.Sleep(2 * stateSyncTestFrameTimeout)
		}
		require.NoError(t, receiver.processAttestationStateBatch(zerolog.Nop(), batch))
	}
	require.Equal(t, prefixBatchEnd, read(prefixBatchEnd))
	write([]byte(p2p.StreamMsgDone))
}

func TestStateSyncSenderProcessingACKDeadlines(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	for _, tc := range []struct {
		name string
		slow int
	}{
		{"inline middle batch", 2},
		{"inline final batch", 3},
		{"legacy receiver", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender := stateSyncDeadlineFixture(t, 3)
			receiver, _, _, _, _, _ := setupTestGossip(t)
			receiver.logger = zerolog.Nop()
			var logs stateSyncLogBuffer
			sender.logger = zerolog.New(&logs)
			server, client := stateSyncDeadlinePair(t)
			done := make(chan struct{})
			go func() { sender.handleStreamAttestationState(server); close(done) }()
			t.Cleanup(func() { _ = server.transport.Close(); waitStateSyncDeadlineHandler(t, done) })
			if tc.slow == 0 {
				receiver.askStreamForState(client)
			} else {
				stateSyncReceiveInline(t, receiver, client, tc.slow)
			}
			waitStateSyncDeadlineHandler(t, done)
			require.Contains(t, logs.String(), "successfully sent attestation state in batches")
			require.NotContains(t, logs.String(), "i/o timeout")
			require.Len(t, receiver.networkFees, 3)
			for fee := range sender.networkFees {
				require.NotNil(t, receiver.networkFees[fee])
				require.Equal(t, 1, receiver.networkFees[fee].AttestationCount())
			}
			var exchangeDeadline time.Time
			for _, ordinary := range []bool{true, true, true, true, false, true, false, true, false} {
				got := stateSyncNextReadDeadline(t, server)
				require.Equal(t, ordinary, got.ordinary)
				if !ordinary {
					if exchangeDeadline.IsZero() {
						exchangeDeadline = got.deadline
					}
					require.Equal(t, exchangeDeadline, got.deadline, "processing must not restart the budget")
				}
			}
		})
	}
}

// Stop at a selected sender ACK read, without sending that ACK.
func stateSyncStallAtACK(t *testing.T, client *stateSyncDeadlineStream, phase string) {
	t.Helper()
	write := func(data []byte) {
		require.NoError(t, p2p.WriteStreamWithBuffer(data, client))
	}
	read := func(prefix []byte) {
		message, err := p2p.ReadStreamWithBuffer(client)
		require.NoError(t, err)
		require.True(t, bytes.HasPrefix(message, prefix))
	}
	write(prefixSendState)
	read(prefixBatchBegin)
	if phase == "begin" {
		return
	}
	write([]byte(streamAckBegin))
	read(prefixBatchHeader)
	if phase == "first header" {
		return
	}
	write([]byte(streamAckHeader))
	read(prefixBatchData)
	if phase == "data" {
		return
	}
	write([]byte(streamAckData))
	if phase == "next header" {
		read(prefixBatchHeader)
	} else {
		require.Equal(t, "final", phase)
		read(prefixBatchEnd)
	}
}

func TestStateSyncSenderOrdinaryACKDeadlines(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	for _, tc := range []struct {
		phase string
		reads int
		log   string
	}{
		{"begin", 2, "failed to read ack after batch begin"},
		{"first header", 3, "read state batch header ACK"},
		{"data", 4, "read state batch data ACK"},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			sender := stateSyncDeadlineFixture(t, 1)
			var logs stateSyncLogBuffer
			sender.logger = zerolog.New(&logs)
			server, client := stateSyncDeadlinePair(t)
			done := make(chan struct{})
			go func() { sender.handleStreamAttestationState(server); close(done) }()
			t.Cleanup(func() { _ = server.transport.Close(); waitStateSyncDeadlineHandler(t, done) })
			stateSyncStallAtACK(t, client, tc.phase)
			var got stateSyncReadDeadline
			for range tc.reads {
				got = stateSyncNextReadDeadline(t, server)
				require.True(t, got.ordinary)
				require.InDelta(t, float64(p2p.TimeoutReadPayload), float64(got.deadline.Sub(got.at)), float64(time.Second))
			}
			waitStateSyncDeadlineHandler(t, done)
			require.GreaterOrEqual(t, time.Since(got.at), stateSyncTestFrameTimeout)
			require.Contains(t, logs.String(), tc.log)
			require.Contains(t, logs.String(), "i/o timeout")
			require.Zero(t, stateSyncExportCount(sender))
		})
	}
}

func TestStateSyncSenderProcessingACKExpiry(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	for _, phase := range []string{"next header", "final"} {
		t.Run(phase, func(t *testing.T) {
			count := 1
			if phase == "next header" {
				count = 2
			}
			sender := stateSyncDeadlineFixture(t, count)
			sender.config.StateSyncTimeout = 3 * stateSyncTestFrameTimeout
			var logs stateSyncLogBuffer
			sender.logger = zerolog.New(&logs)
			server, client := stateSyncDeadlinePair(t)
			done := make(chan struct{})
			go func() { sender.handleStreamAttestationState(server); close(done) }()
			t.Cleanup(func() { _ = server.transport.Close(); waitStateSyncDeadlineHandler(t, done) })
			stateSyncStallAtACK(t, client, phase)
			for range 4 {
				require.True(t, stateSyncNextReadDeadline(t, server).ordinary)
			}
			got := stateSyncNextReadDeadline(t, server)
			require.False(t, got.ordinary)
			waitStateSyncDeadlineHandler(t, done)
			require.False(t, time.Now().Before(got.deadline))
			require.NotContains(t, logs.String(), "successfully sent attestation state")
		})
	}
}

func TestStateSyncSenderBudgetIncludesSnapshotConstruction(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	sender := stateSyncDeadlineFixture(t, 1)
	sender.config.StateSyncTimeout = time.Second
	server, client := stateSyncDeadlinePair(t)
	sender.mu.Lock()
	locked := true
	done := make(chan struct{})
	go func() { sender.handleStreamAttestationState(server); close(done) }()
	t.Cleanup(func() {
		if locked {
			sender.mu.Unlock()
		}
		_ = server.transport.Close()
		waitStateSyncDeadlineHandler(t, done)
	})
	require.NoError(t, p2p.WriteStreamWithBuffer(prefixSendState, client))
	require.Eventually(t, func() bool { return server.resets.Load()+server.closes.Load() > 0 }, 3*time.Second, time.Millisecond,
		"budget must expire even while snapshot construction waits on its lock")
	// S15 makes the snapshot lock wait cancellable. The owner must exit and
	// release its slot while the mutex is still held by this test.
	waitStateSyncDeadlineHandler(t, done)
	require.Zero(t, stateSyncExportCount(sender))
	sender.mu.Unlock()
	locked = false
}

func TestStateSyncSenderDeadlineCancellationAndFallback(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second, 5 * time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			sender := stateSyncDeadlineFixture(t, 0)
			sender.config.StateSyncTimeout = timeout
			server, _ := stateSyncDeadlinePair(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			before := time.Now()
			exchange, stop := sender.stateExchangeDeadline(ctx, server)
			deadline, _ := exchange.Deadline()
			defer stop()
			want := timeout
			if want <= 0 {
				want = 30 * time.Minute
			}
			require.False(t, deadline.Before(before.Add(want)))
			require.False(t, deadline.After(time.Now().Add(want)))
			cancel()
			require.Eventually(t, func() bool { return server.resets.Load() > 0 }, time.Second, time.Millisecond)
		})
	}
	sender := stateSyncDeadlineFixture(t, 0)
	server, _ := stateSyncDeadlinePair(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, stop := sender.stateExchangeDeadline(ctx, server)
	stop()
	cancel()
	time.Sleep(20 * time.Millisecond)
	require.Zero(t, server.resets.Load(), "finished transfers must disarm their reset")
}

func stateSyncExportCount(s *AttestationGossip) int {
	s.stateExportMu.Lock()
	defer s.stateExportMu.Unlock()
	return len(s.stateExports)
}

func startStateSyncExport(t *testing.T, sender *AttestationGossip, remote peer.ID) (*stateSyncDeadlineStream, *stateSyncDeadlineStream, <-chan struct{}) {
	t.Helper()
	server, client := stateSyncDeadlinePair(t)
	server.peer = remote
	done := make(chan struct{})
	go func() { sender.handleStreamAttestationState(server); close(done) }()
	t.Cleanup(func() { _ = server.transport.Close(); waitStateSyncDeadlineHandler(t, done) })
	return server, client, done
}

func TestStateSyncExportAdmission(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	sender := stateSyncDeadlineFixture(t, 2)
	_, a, doneA := startStateSyncExport(t, sender, "a")
	stateSyncStallAtACK(t, a, "next header")
	require.Equal(t, 1, stateSyncExportCount(sender))

	// A duplicate must not consume the other peer's free slot.
	duplicate, duplicateClient, duplicateDone := startStateSyncExport(t, sender, "a")
	require.NoError(t, p2p.WriteStreamWithBuffer(prefixSendState, duplicateClient))
	waitStateSyncDeadlineHandler(t, duplicateDone)
	require.Positive(t, duplicate.resets.Load())
	require.Equal(t, 1, stateSyncExportCount(sender))
	receiver := stateSyncDeadlineFixture(t, 0)
	_, b, doneB := startStateSyncExport(t, sender, "b")
	require.True(t, receiver.askStreamForState(b).completed)
	waitStateSyncDeadlineHandler(t, doneB)
	require.Len(t, receiver.networkFees, 2)
	require.Equal(t, 1, stateSyncExportCount(sender))

	_, b, doneB = startStateSyncExport(t, sender, "b")
	stateSyncStallAtACK(t, b, "next header")
	require.Equal(t, 2, stateSyncExportCount(sender))

	// With both copies held, rejection must not wait for the snapshot lock.
	sender.mu.Lock()
	locked := true
	t.Cleanup(func() {
		if locked {
			sender.mu.Unlock()
		}
	})
	rejected, c, doneC := startStateSyncExport(t, sender, "c")
	require.NoError(t, p2p.WriteStreamWithBuffer(prefixSendState, c))
	select {
	case <-doneC:
	case <-time.After(time.Second):
		sender.mu.Unlock()
		locked = false
		t.Fatal("full capacity reached the snapshot lock")
	}
	require.Positive(t, rejected.resets.Load())
	require.Equal(t, 2, stateSyncExportCount(sender))
	sender.mu.Unlock()
	locked = false
	require.NoError(t, a.Reset())
	require.NoError(t, b.Reset())
	waitStateSyncDeadlineHandler(t, doneA)
	waitStateSyncDeadlineHandler(t, doneB)
	require.Zero(t, stateSyncExportCount(sender))
}

func TestStateSyncExportReleaseOnEveryExit(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	for _, phase := range []string{"begin", "next header", "final", "bad ack", "reset", "success", "empty"} {
		t.Run(phase, func(t *testing.T) {
			count := 1
			if phase == "next header" {
				count = 2
			}
			if phase == "empty" {
				count = 0
			}
			sender := stateSyncDeadlineFixture(t, count)
			sender.config.StateSyncTimeout = time.Second
			// Build receivers before the handler starts: the sender's first
			// read is scaled to one second, and fixture setup can exceed it.
			receiver := stateSyncDeadlineFixture(t, 0)
			_, client, done := startStateSyncExport(t, sender, "peer")
			if phase == "success" || phase == "empty" {
				require.True(t, receiver.askStreamForState(client).completed)
			} else {
				stall := phase
				if phase == "bad ack" || phase == "reset" {
					stall = "begin"
				}
				stateSyncStallAtACK(t, client, stall)
				require.Equal(t, 1, stateSyncExportCount(sender))
				switch phase {
				case "bad ack":
					require.NoError(t, p2p.WriteStreamWithBuffer([]byte("wrong"), client))
				case "reset":
					require.NoError(t, client.Reset())
				}
			}
			waitStateSyncDeadlineHandler(t, done)
			require.Zero(t, stateSyncExportCount(sender))
			// The same identity can obtain a new copy after any exit.
			sender.config.StateSyncTimeout = 15 * time.Second
			retryReceiver := stateSyncDeadlineFixture(t, 0)
			_, retry, retryDone := startStateSyncExport(t, sender, "peer")
			require.True(t, retryReceiver.askStreamForState(retry).completed)
			waitStateSyncDeadlineHandler(t, retryDone)
			require.Zero(t, stateSyncExportCount(sender))
		})
	}
}

// A small framed reply or a blocked read. Cancellation is exercised through
// Reset, rather than pretending deadline setters interrupt a bytes.Buffer.
type startupRetryStream struct {
	MockStream
	data                *bytes.Reader
	started, stopped    chan struct{}
	startOnce, stopOnce sync.Once
	gate, join          <-chan struct{}
	block               bool
	resets              atomic.Int32
}

func newStartupRetryStream(remote peer.ID, empty bool) *startupRetryStream {
	var frame []byte
	if empty {
		payload := append(append([]byte(nil), prefixBatchBegin...), 0, 0, 0, 0)
		frame = make([]byte, 4+len(payload))
		binary.LittleEndian.PutUint32(frame, uint32(len(payload)))
		copy(frame[4:], payload)
	}
	return &startupRetryStream{
		MockStream: MockStream{peer: remote}, data: bytes.NewReader(frame),
		started: make(chan struct{}), stopped: make(chan struct{}),
	}
}

func (s *startupRetryStream) Read(p []byte) (int, error) {
	s.startOnce.Do(func() { close(s.started) })
	if s.block {
		<-s.stopped
		if s.join != nil {
			<-s.join
		}
		return 0, io.ErrClosedPipe
	}
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-s.stopped:
			return 0, io.ErrClosedPipe
		}
	}
	return s.data.Read(p)
}

func (s *startupRetryStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *startupRetryStream) Reset() error {
	s.resets.Add(1)
	s.stopOnce.Do(func() { close(s.stopped) })
	return nil
}

func (s *startupRetryStream) Close() error {
	s.stopOnce.Do(func() { close(s.stopped) })
	return nil
}

func TestStartupStateSyncBusyThenRecovers(t *testing.T) {
	stateSyncDeadlineTestSettings(t)
	sender := stateSyncDeadlineFixture(t, 2)
	_, stalledA, doneA := startStateSyncExport(t, sender, "holder-a")
	_, stalledB, doneB := startStateSyncExport(t, sender, "holder-b")
	stateSyncStallAtACK(t, stalledA, "next header")
	stateSyncStallAtACK(t, stalledB, "next header")
	require.Equal(t, 2, stateSyncExportCount(sender))

	requesters := make([]*AttestationGossip, 3)
	exits := make(chan chan struct{}, 20)
	for i := range requesters {
		g, h := startupRetryGossip(t, "server")
		requesters[i] = g
		// Prepare sockets on the test goroutine; the production dial callback
		// only consumes prepared pairs and starts the real remote handler.
		pairs := make(chan [2]*stateSyncDeadlineStream, 5)
		for range 5 {
			server, client := stateSyncDeadlinePair(t)
			server.peer = peer.ID(fmt.Sprintf("requester-%d", i))
			pairs <- [2]*stateSyncDeadlineStream{server, client}
		}
		h.streamFunc = func(context.Context, peer.ID, ...protocol.ID) (network.Stream, error) {
			select {
			case pair := <-pairs:
				done := make(chan struct{})
				exits <- done
				go func() { sender.handleStreamAttestationState(pair[0]); close(done) }()
				return pair[1], nil
			default:
				return nil, fmt.Errorf("retry socket budget exhausted")
			}
		}
	}
	joinSenders := func() {
		for len(exits) > 0 {
			waitStateSyncDeadlineHandler(t, <-exits)
		}
	}
	now := time.Now()
	for _, g := range requesters {
		g.tickAttestationStateSync(context.Background(), true, true, now)
	}
	for _, g := range requesters {
		settleStartupRetry(t, g, now)
		require.Equal(t, 1, g.startupState.result.failed)
		require.False(t, g.startupState.finished)
	}
	joinSenders()
	require.NoError(t, stalledA.Reset())
	require.NoError(t, stalledB.Reset())
	waitStateSyncDeadlineHandler(t, doneA)
	waitStateSyncDeadlineHandler(t, doneB)
	for range 4 {
		for _, g := range requesters {
			if !g.startupState.finished {
				g.tickAttestationStateSync(context.Background(), true, false, g.startupState.next)
			}
		}
		for _, g := range requesters {
			if !g.startupState.finished {
				settleStartupRetry(t, g, g.startupState.next)
			}
		}
		joinSenders()
	}
	for _, g := range requesters {
		require.True(t, g.startupState.finished)
		require.Equal(t, 1, g.startupState.result.nonEmpty)
		require.LessOrEqual(t, g.startupState.requests, 5)
		require.Len(t, g.networkFees, 2)
		for _, state := range g.networkFees {
			require.Equal(t, 1, state.AttestationCount())
		}
	}
	require.Zero(t, stateSyncExportCount(sender))
}

func TestStartupStateSyncWholeGroupAndEmptyRetry(t *testing.T) {
	g, h := startupRetryGossip(t, "empty", "failed", "slow")
	gate := make(chan struct{})
	defer close(gate)
	slow := newStartupRetryStream("slow", true)
	slow.gate = gate
	empty := newStartupRetryStream("empty", true)
	h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		switch id {
		case "slow":
			return slow, nil
		case "empty":
			return empty, nil
		default:
			return nil, fmt.Errorf("busy")
		}
	}
	now := time.Now()
	g.tickAttestationStateSync(context.Background(), true, true, now)
	waitStateSyncDeadlineHandler(t, slow.started)
	waitStateSyncDeadlineHandler(t, empty.stopped)
	for range 3 {
		g.tickAttestationStateSync(context.Background(), true, false, now.Add(time.Minute))
	}
	require.Equal(t, 3, g.startupState.requests)
	require.False(t, g.startupState.finished)
	require.Zero(t, slow.resets.Load(), "a completed peer must not cancel another import")
	// Reset ends the still-running peer; an empty response does not stop collection.
	require.NoError(t, slow.Reset())
	settleStartupRetry(t, g, now.Add(time.Minute))
	require.False(t, g.startupState.finished)
	require.Equal(t, attestationStateGroupResult{empty: 1, failed: 2}, g.startupState.result)
}

func TestStartupStateSyncFreshPeersAndRequestBudget(t *testing.T) {
	g, h := startupRetryGossip(t, "a", "b", "c", "d")
	g.config.AskPeers = 2
	calls := make(chan peer.ID, 10)
	h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		calls <- id
		return nil, fmt.Errorf("busy")
	}
	now := time.Now()
	g.startupState = newAttestationStateCollection(context.Background(), now, time.Hour, 5)
	deadline := g.startupState.deadline
	for _, count := range []int{2, 4, 5} {
		dispatch := g.startupState.next
		g.tickAttestationStateSync(context.Background(), true, false, dispatch)
		require.Equal(t, count, g.startupState.requests)
		settleStartupRetry(t, g, dispatch)
		require.Equal(t, deadline, g.startupState.deadline, "retries must not renew the collection window")
		ctxDeadline, _ := g.startupState.ctx.Deadline()
		require.Equal(t, deadline, ctxDeadline)
		if count < 5 {
			g.tickAttestationStateSync(context.Background(), true, false, dispatch.Add(time.Second))
			require.Nil(t, g.startupState.group, "all retry groups require backoff")
		}
	}
	require.True(t, g.startupState.finished)
	require.Equal(t, attestationStateGroupResult{failed: 5}, g.startupState.result)
	seen := map[peer.ID]bool{}
	for range 4 {
		seen[<-calls] = true
	}
	require.Len(t, seen, 4)
	require.True(t, seen[<-calls])
	old := g.startupState
	g.tickAttestationStateSync(context.Background(), true, false, now.Add(2*time.Hour))
	require.Same(t, old, g.startupState)
	require.Equal(t, 5, g.startupState.requests)

	// A peer discovered after reuse still precedes any previously asked peer.
	c := newAttestationStateCollection(context.Background(), now, time.Hour, 50)
	defer c.cancel()
	c.selectPeers([]peer.ID{"a"}, 1)
	c.selectPeers([]peer.ID{"a"}, 1)
	require.Equal(t, []peer.ID{"new"}, c.selectPeers([]peer.ID{"a", "new"}, 2))
}

func TestStartupStateSyncCancellationAndWindow(t *testing.T) {
	for _, mode := range []string{"parent", "shutdown", "leave", "window", "attempt"} {
		t.Run(mode, func(t *testing.T) {
			g, h := startupRetryGossip(t, "peer")
			stream := newStartupRetryStream("peer", false)
			stream.block = true
			var dialDeadline time.Time
			h.streamFunc = func(ctx context.Context, _ peer.ID, _ ...protocol.ID) (network.Stream, error) {
				dialDeadline, _ = ctx.Deadline()
				return stream, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			now := time.Now()
			window := time.Hour
			if mode == "window" {
				window = 50 * time.Millisecond
			}
			if mode == "attempt" {
				g.config.StateSyncTimeout = 50 * time.Millisecond
			}
			g.startupState = newAttestationStateCollection(ctx, now, window, 50)
			g.tickAttestationStateSync(ctx, true, false, now)
			group := g.startupState.group
			waitStateSyncDeadlineHandler(t, stream.started)
			switch mode {
			case "parent":
				cancel()
			case "shutdown":
				g.stopStartupStateSync()
			case "leave":
				g.tickAttestationStateSync(ctx, false, false, now)
			}
			waitStateSyncDeadlineHandler(t, group.done)
			g.tickAttestationStateSync(ctx, true, false, now)
			require.Positive(t, stream.resets.Load())
			require.Equal(t, 1, g.startupState.requests)
			require.Equal(t, mode != "attempt", g.startupState.finished)
			if mode == "window" {
				require.Equal(t, g.startupState.deadline, dialDeadline)
			}
			if mode == "attempt" {
				require.True(t, dialDeadline.Before(g.startupState.deadline))
			}
		})
	}
}

func TestStartupStateSyncReactivationJoinsPriorGroup(t *testing.T) {
	g, h := startupRetryGossip(t, "peer")
	join := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(join) }) }
	defer release()
	stream := newStartupRetryStream("peer", false)
	stream.block, stream.join = true, join
	var calls atomic.Int32
	h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		if calls.Add(1) == 1 {
			return stream, nil
		}
		return newStartupRetryStream(id, true), nil
	}
	now := time.Now()
	g.tickAttestationStateSync(context.Background(), true, true, now)
	old := g.startupState
	waitStateSyncDeadlineHandler(t, stream.started)
	g.tickAttestationStateSync(context.Background(), false, false, now)
	waitStateSyncDeadlineHandler(t, stream.stopped)
	activated := now.Add(time.Second)
	g.tickAttestationStateSync(context.Background(), true, true, activated)
	require.Same(t, old, g.startupState)
	require.EqualValues(t, 1, calls.Load())
	release()
	waitStateSyncDeadlineHandler(t, old.group.done)
	g.tickAttestationStateSync(context.Background(), true, false, activated.Add(time.Minute))
	require.NotSame(t, old, g.startupState)
	require.Equal(t, activated.Add(startupStateSyncWindow), g.startupState.deadline)
	settleStartupRetry(t, g, activated.Add(time.Minute))
	require.False(t, g.startupState.finished)
	require.Equal(t, 1, g.startupState.result.empty)
	require.EqualValues(t, 2, calls.Load())
}

func TestStartupStateSyncNoPeersAndJitter(t *testing.T) {
	g, _ := startupRetryGossip(t)
	now := time.Now()
	g.startupState = newAttestationStateCollection(context.Background(), now, 30*time.Millisecond, 50)
	g.tickAttestationStateSync(context.Background(), true, false, now)
	require.Nil(t, g.startupState.group)
	require.Zero(t, g.startupState.requests)
	waitStateSyncDeadlineHandler(t, g.startupState.ctx.Done())
	g.tickAttestationStateSync(context.Background(), true, false, now)
	require.True(t, g.startupState.finished)
	for _, round := range []int{1, 2, 10, 1000} {
		for _, random := range []float64{0, 0.5, 1} {
			delay := attestationStateRetryDelay(round, random)
			require.GreaterOrEqual(t, delay, 2500*time.Millisecond)
			require.LessOrEqual(t, delay, time.Minute)
		}
	}
}

// Exercise the actual startup group and Observer.Stop, including the production
// bridge's HTTP cancellation and its wait behind a legacy cached request.
func TestStartupStateSyncObserverStopCancelsVaultLookup(t *testing.T) {
	for _, phase := range []string{"request", "cache wait", "inbound request"} {
		t.Run(phase, func(t *testing.T) {
			g, h, _, _, legacy, _ := setupTestGossip(t)
			g.config.AskPeers = 1
			g.config.StateSyncTimeout = time.Hour // Only shutdown should end this import.
			g.activeVals = map[peer.ID]bool{h.peers[1]: true}
			tx, _ := observerTestXMRPlan(t)
			batch := signedCancellationBatch(t, tx, secp256k1.GenPrivKey())
			data, err := batch.Marshal()
			require.NoError(t, err)
			stream := newStateBoundsStream(h.peers[1], append([]byte{prefixBatchBegin[0]}, stateBoundsCount(1)...), stateBoundsHeader(0, uint32(len(data))), append([]byte{prefixBatchData[0]}, data...), prefixBatchEnd)
			dialStarted := make(chan struct{})
			h.streamFunc = func(ctx context.Context, _ peer.ID, _ ...protocol.ID) (network.Stream, error) {
				if phase == "inbound request" {
					close(dialStarted)
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return stream, nil
			}

			requestStarted, requestExited := make(chan struct{}), make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					close(requestStarted)
					defer close(requestExited)
				}
				select {
				case <-r.Context().Done():
				case <-release:
					_, _ = w.Write([]byte("{}"))
				}
			}))
			t.Cleanup(server.Close)
			t.Cleanup(unblock)
			bridge, err := thorclient.NewThorchainBridge(config.BifrostClientConfiguration{ChainID: "thorchain", ChainHost: server.URL}, getSharedTestMetrics(), nil)
			require.NoError(t, err)
			queries, ok := bridge.(thorclient.ContextQueryBridge)
			require.True(t, ok)
			lookupStarted, lookupExited := make(chan struct{}), make(chan struct{})
			lookupCancelled, releaseLookup := make(chan struct{}), make(chan struct{})
			var releaseLookupOnce sync.Once
			finishLookup := func() { releaseLookupOnce.Do(func() { close(releaseLookup) }) }
			t.Cleanup(finishLookup)
			g.bridge = &cancellationQueryBridge{MockThorchainBridge: legacy, vault: func(ctx context.Context, key string) (thortypes.Vault, error) {
				close(lookupStarted)
				defer close(lookupExited)
				vault, queryErr := queries.GetVaultWithContext(ctx, key)
				if phase == "inbound request" {
					close(lookupCancelled)
					<-releaseLookup
				}
				return vault, queryErr
			}}
			// If state processing regresses to the legacy API, it must hit the same
			// stalled request rather than silently failing before the tested decision.
			legacy.getVaultFunc = func(key string) (thortypes.Vault, error) {
				close(lookupStarted)
				defer close(lookupExited)
				return bridge.GetVault(key)
			}
			if phase == "cache wait" {
				legacyDone := make(chan struct{})
				go func() { _, _ = bridge.GetVault(tx.ObservedPubKey.String()); close(legacyDone) }()
				t.Cleanup(func() { unblock(); waitStateBounds(t, legacyDone) })
				waitStateBounds(t, requestStarted)
			}
			pkm, err := pubkeymanager.NewPubKeyManager(nil, nil)
			require.NoError(t, err)
			o, err := NewObserver(pkm, nil, legacy, getSharedTestMetrics(), t.TempDir(), metrics.NewTssKeysignMetricMgr(), g, "")
			require.NoError(t, err)
			var imports atomic.Int32
			g.SetObserverApplySpentOutputRefs(func(spentOutputRefsImport, func(bool)) { imports.Add(1) })
			stopped := make(chan struct{})
			var stopOnce sync.Once
			var stopErr error
			stop := func() { stopOnce.Do(func() { go func() { stopErr = o.Stop(); close(stopped) }() }) }
			t.Cleanup(func() { unblock(); finishLookup(); stop(); waitStateBounds(t, stopped) })
			g.tickAttestationStateSync(context.Background(), true, true, time.Now())
			group := g.startupState.group
			require.NotNil(t, group)
			var inboundDone chan struct{}
			if phase == "inbound request" {
				waitStateBounds(t, dialStarted)
				inboundDone = make(chan struct{})
				go func() { g.handleStreamAttestationState(stream); close(inboundDone) }()
				t.Cleanup(func() { unblock(); finishLookup(); stop(); waitStateBounds(t, inboundDone) })
			}
			waitStateBounds(t, lookupStarted)
			waitStateBounds(t, requestStarted)
			stop()
			if inboundDone != nil {
				waitStateBounds(t, lookupCancelled)
				select {
				case <-stopped:
					t.Fatal("observer stopped before joining its inbound import")
				case <-time.After(50 * time.Millisecond):
				}
				_, err = o.storage.db.Has([]byte("open-until-inbound-join"), nil)
				require.NoError(t, err)
				finishLookup()
			}
			select {
			case <-stopped:
				require.NoError(t, stopErr)
			case <-time.After(3 * time.Second):
				t.Fatal("Observer.Stop did not cancel and join the stalled vault lookup")
			}
			waitStateBounds(t, lookupExited)
			waitStateBounds(t, group.done)
			if inboundDone != nil {
				waitStateBounds(t, inboundDone)
			}
			require.True(t, g.startupState.finished)
			require.True(t, g.startupStateStopped)
			require.Zero(t, group.result.nonEmpty)
			require.Positive(t, stream.resets.Load())
			require.Zero(t, imports.Load(), "cancelled authorization must not launch an import")
			require.Zero(t, cancellationTestReservationCount(g))
			_, err = o.storage.db.Has([]byte("closed-after-join"), nil)
			require.Error(t, err)
			if phase != "cache wait" {
				waitStateBounds(t, requestExited)
			} else {
				select {
				case <-requestExited:
					t.Fatal("cancelling the cache waiter interrupted the unrelated legacy request")
				default:
				}
			}
			require.EqualValues(t, 1, requests.Load(), "cache wait cancellation must not launch a second request")
		})
	}
}
