package tss

import (
	"encoding/base64"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/blang/semver"
	"github.com/cometbft/cometbft/crypto"
	sdkTypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/tendermint/btcd/btcec"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keysign"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	maxKeysignPerRequest = 15 // the maximum number of messages include in one single TSS keysign request
	// RemoteSignTimeout is the maximum time Bifrost waits for a TSS keysign result.
	// Callers that add an outer watchdog must derive it from this value.
	RemoteSignTimeout = 5 * time.Minute
)

// KeySignBatchInterval is how long the task queue must stay quiet before the
// queued messages are sent to TSS. Exposed so tests with a fake TSS server can
// skip the real waiting.
var KeySignBatchInterval = time.Second

type tssServer interface {
	KeySign(req keysign.Request) (keysign.Response, error)
}

// KeySign is a proxy between signer and TSS
type KeySign struct {
	logger         zerolog.Logger
	server         tssServer
	bridge         thorclient.ThorchainBridge
	currentVersion semver.Version
	lastCheck      time.Time
	wg             *sync.WaitGroup
	taskQueue      chan *tssKeySignTask
	done           chan struct{}
}

// NewKeySign create a new instance of KeySign
func NewKeySign(server tssServer, bridge thorclient.ThorchainBridge) (*KeySign, error) {
	return &KeySign{
		server:    server,
		bridge:    bridge,
		logger:    log.With().Str("module", "tss_signer").Logger(),
		wg:        &sync.WaitGroup{},
		taskQueue: make(chan *tssKeySignTask),
		done:      make(chan struct{}),
	}, nil
}

// GetPrivKey THORNode don't actually have any private key , but just return something
func (s *KeySign) GetPrivKey() crypto.PrivKey {
	return nil
}

func (s *KeySign) GetAddr() sdkTypes.AccAddress {
	return nil
}

// ExportAsMnemonic THORNode don't need this function for TSS, just keep it to fulfill KeyManager interface
func (s *KeySign) ExportAsMnemonic() (string, error) {
	return "", nil
}

// ExportAsPrivateKey THORNode don't need this function for TSS, just keep it to fulfill KeyManager interface
func (s *KeySign) ExportAsPrivateKey() (string, error) {
	return "", nil
}

// ExportAsKeyStore THORNode don't need this function for TSS, just keep it to fulfill KeyManager interface
func (s *KeySign) ExportAsKeyStore(password string) (*EncryptedKeyJSON, error) {
	return nil, nil
}

// Start the keysign workers
func (s *KeySign) Start() {
	s.wg.Add(1)
	go s.processKeySignTasks()
}

// Stop Keysign
func (s *KeySign) Stop() {
	close(s.done)
	s.wg.Wait()
	close(s.taskQueue)
}

// RemoteSign sends the request to the local task queue with the caller's
// consensus-scheduled THORChain height.
func (s *KeySign) RemoteSign(msg []byte, algo common.SigningAlgo, poolPubKey string, thorchainHeight int64) ([]byte, []byte, error) {
	if len(msg) == 0 {
		return nil, nil, nil
	}

	encodedMsg := base64.StdEncoding.EncodeToString(msg)
	task := tssKeySignTask{
		Algo:       algo,
		PoolPubKey: poolPubKey,
		Msg:        encodedMsg,
		Height:     thorchainHeight,
		Resp:       make(chan tssKeySignResult, 1),
	}
	s.taskQueue <- &task
	select {
	case resp := <-task.Resp:
		if resp.Err != nil {
			return nil, nil, fmt.Errorf("fail to tss sign: %w", resp.Err)
		}

		if len(resp.R) == 0 && len(resp.S) == 0 {
			// this means the node tried to do keysign , however this node has not been chosen to take part in the keysign committee
			return nil, nil, ErrNotSelectedForKeysign
		}
		if algo == common.SigningAlgoEd25519 {
			// ed25519 signature is not R and S , but a single byte array
			sBytes, err := base64.StdEncoding.DecodeString(resp.S)
			if err != nil {
				return nil, nil, err
			}
			return sBytes, nil, nil
		}
		s.logger.Debug().Str("R", resp.R).Str("S", resp.S).Str("recovery", resp.RecoveryID).Msg("tss result")
		data, err := getSignature(resp.R, resp.S)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to decode tss signature: %w", err)
		}
		bRecoveryId, err := base64.StdEncoding.DecodeString(resp.RecoveryID)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to decode recovery id: %w", err)
		}
		return data, bRecoveryId, nil
	case <-time.After(RemoteSignTimeout):
		return nil, nil, fmt.Errorf("TIMEOUT: fail to sign message:%s after %d minutes", encodedMsg, int(RemoteSignTimeout/time.Minute))
	}
}

type tssKeySignTask struct {
	PoolPubKey string
	Algo       common.SigningAlgo
	Msg        string
	Height     int64
	Resp       chan tssKeySignResult
}

type tssKeySignBatchKey struct {
	algo   common.SigningAlgo
	height int64
}

type tssKeySignResult struct {
	R          string
	S          string
	RecoveryID string
	Err        error
}

func (s *KeySign) processKeySignTasks() {
	defer s.wg.Done()
	tasks := make(map[string][]*tssKeySignTask)
	taskLock := sync.Mutex{}
	for {
		select {
		case <-s.done:
			// requested to exit
			return
		case t := <-s.taskQueue:
			taskLock.Lock()
			_, ok := tasks[t.PoolPubKey]
			if !ok {
				tasks[t.PoolPubKey] = []*tssKeySignTask{
					t,
				}
			} else {
				tasks[t.PoolPubKey] = append(tasks[t.PoolPubKey], t)
			}
			taskLock.Unlock()
		case <-time.After(KeySignBatchInterval):
			// This implementation will check the tasks every second , and send whatever is in the queue to TSS
			// if it has more than maxKeysignPerRequest(15) in the queue , it will only send the first maxKeysignPerRequest(15) of them
			// the reset will be send in the next request
			taskLock.Lock()
			for poolPubKey, v := range tasks {
				if len(v) == 0 {
					delete(tasks, poolPubKey)
					continue
				}
				totalTasks := len(v)
				// send no more than maxKeysignPerRequest messages in a single TSS keysign request
				// SECURITY FIX: Always sort tasks to ensure deterministic ordering across all nodes.
				// The order of messages in the slice is non-deterministic due to goroutine execution timing.
				// All bifrosts MUST send messages in the same order for TSS signature pairing to work correctly.
				sort.SliceStable(v, func(i, j int) bool {
					if v[i].Height != v[j].Height {
						return v[i].Height < v[j].Height
					}
					if v[i].Algo != v[j].Algo {
						return string(v[i].Algo) < string(v[j].Algo)
					}
					return v[i].Msg < v[j].Msg
				})

				if totalTasks > maxKeysignPerRequest {
					totalTasks = maxKeysignPerRequest
				}
				signingTask := v[:totalTasks]
				tasks[poolPubKey] = v[totalTasks:]

				// Split by algo and scheduled THORChain height. A single go-tss
				// request has one BlockHeight, so do not batch TxOut items with
				// different policy heights together.
				batches := make(map[tssKeySignBatchKey][]*tssKeySignTask)
				batchKeys := make([]tssKeySignBatchKey, 0)
				for _, task := range signingTask {
					switch task.Algo {
					case common.SigningAlgoSecp256k1:
					case common.SigningAlgoEd25519:
					default:
						s.setTssKeySignTasksFail([]*tssKeySignTask{task}, fmt.Errorf("unknown signing algo %s", task.Algo))
						continue
					}
					key := tssKeySignBatchKey{algo: task.Algo, height: task.Height}
					if _, ok := batches[key]; !ok {
						batchKeys = append(batchKeys, key)
					}
					batches[key] = append(batches[key], task)
				}
				sort.Slice(batchKeys, func(i, j int) bool {
					if batchKeys[i].height != batchKeys[j].height {
						return batchKeys[i].height < batchKeys[j].height
					}
					return string(batchKeys[i].algo) < string(batchKeys[j].algo)
				})
				for _, key := range batchKeys {
					s.wg.Add(1)
					go s.toLocalTSSSigner(poolPubKey, key.algo, batches[key])
				}
			}
			taskLock.Unlock()
		}
	}
}

func getSignature(r, s string) ([]byte, error) {
	rBytes, err := base64.StdEncoding.DecodeString(r)
	if err != nil {
		return nil, err
	}
	sBytes, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}

	R := new(big.Int).SetBytes(rBytes)
	S := new(big.Int).SetBytes(sBytes)
	N := btcec.S256().N
	halfOrder := new(big.Int).Rsh(N, 1)
	// see: https://github.com/ethereum/go-ethereum/blob/f9401ae011ddf7f8d2d95020b7446c17f8d98dc1/crypto/signature_nocgo.go#L90-L93
	if S.Cmp(halfOrder) == 1 {
		S.Sub(N, S)
	}

	// Serialize signature to R || S.
	// R, S are padded to 32 bytes respectively.
	rBytes = R.Bytes()
	sBytes = S.Bytes()

	sigBytes := make([]byte, 64)
	// 0 pad the byte arrays from the left if they aren't big enough.
	copy(sigBytes[32-len(rBytes):32], rBytes)
	copy(sigBytes[64-len(sBytes):64], sBytes)
	return sigBytes, nil
}

func (s *KeySign) getVersion() semver.Version {
	requestTime := time.Now()
	// analyze-ignore(float-comparison)
	if !s.currentVersion.Equals(semver.Version{}) && requestTime.Sub(s.lastCheck).Seconds() < constants.ThorchainBlockTime.Seconds() {
		return s.currentVersion
	}
	version, err := s.bridge.GetThorchainVersion()
	if err != nil {
		s.logger.Err(err).Msg("fail to get current thorchain version")
		return s.currentVersion
	}
	s.currentVersion = version
	s.lastCheck = requestTime
	return s.currentVersion
}

func (s *KeySign) setTssKeySignTasksFail(tasks []*tssKeySignTask, err error) {
	for _, item := range tasks {
		select {
		case item.Resp <- tssKeySignResult{
			Err: err,
		}:
		case <-time.After(time.Second):
			// this is a fallback , if fail to send a failed result back to caller , it doesn't stuck
			continue
		}
	}
}

// toLocalTSSSigner will send the request to local signer.
func (s *KeySign) toLocalTSSSigner(poolPubKey string, algo common.SigningAlgo, tasks []*tssKeySignTask) {
	defer s.wg.Done()
	tssMsg, err := s.buildKeysignRequest(poolPubKey, algo, tasks)
	if err != nil {
		s.setTssKeySignTasksFail(tasks, err)
		return
	}
	s.logger.Debug().Msg("new TSS join party")
	s.logger.Info().Msgf("msgToSign to tss Local node PoolPubKey: %s, Messages: %+v, block height: %d", tssMsg.PoolPubKey, tssMsg.Messages, tssMsg.BlockHeight)

	keySignResp, err := s.server.KeySign(tssMsg)
	if err != nil {
		s.setTssKeySignTasksFail(tasks, fmt.Errorf("fail tss keysign: %w", err))
		return
	}
	if len(keySignResp.Signatures) == 0 && keySignResp.Blame.FailReason == "" && len(keySignResp.Blame.BlameNodes) == 0 {
		// An empty response means this node was not selected for the filtered
		// keysign party. Report an empty result so callers stand down cleanly.
		s.logger.Info().
			Str("pool_pub_key", poolPubKey).
			Str("algo", string(algo)).
			Int64("block_height", tssMsg.BlockHeight).
			Int("message_count", len(tasks)).
			Msg("local node is not selected for keysign party; standing down")
		for _, t := range tasks {
			t.Resp <- tssKeySignResult{}
		}
		return
	}

	// 1 means success,2 means fail , 0 means NA
	if keySignResp.Status == 1 && len(keySignResp.Blame.BlameNodes) == 0 {
		s.logger.Info().Msgf("response: %+v", keySignResp)
		// success
		for _, t := range tasks {
			found := false
			for _, sig := range keySignResp.Signatures {
				if t.Msg == sig.Msg {
					t.Resp <- tssKeySignResult{
						R:          sig.R,
						S:          sig.S,
						RecoveryID: sig.RecoveryID,
						Err:        nil,
					}
					found = true
					break
				}
			}
			// Didn't find the signature in the tss keysign result , notify the task , so it doesn't get stuck
			if !found {
				t.Resp <- tssKeySignResult{
					Err: fmt.Errorf("didn't find signature for message %s in the keysign result", t.Msg),
				}
			}
		}
		return
	}

	// copy blame to our own struct
	blame := types.Blame{
		FailReason: keySignResp.Blame.FailReason,
		Round:      types.CanonicalTssRound(keySignResp.Blame.Round, algo),
		IsUnicast:  keySignResp.Blame.IsUnicast,
		BlameNodes: make([]types.Node, len(keySignResp.Blame.BlameNodes)),
	}
	for i, n := range keySignResp.Blame.BlameNodes {
		blame.BlameNodes[i].Pubkey = n.Pubkey
		blame.BlameNodes[i].BlameData = n.BlameData
		blame.BlameNodes[i].BlameSignature = n.BlameSignature
	}

	// Blame need to be passed back to thorchain , so as thorchain can use the information to slash relevant node account
	s.setTssKeySignTasksFail(tasks, NewKeysignError(blame))
}

// buildKeysignRequest builds the go-tss request and attaches the signer policy
// for the THORChain height used by the TSS ceremony.
func (s *KeySign) buildKeysignRequest(poolPubKey string, algo common.SigningAlgo, tasks []*tssKeySignTask) (keysign.Request, error) {
	var msgToSign []string
	for _, item := range tasks {
		msgToSign = append(msgToSign, item.Msg)
	}
	tssMsg := keysign.Request{
		Algo:       string(algo),
		PoolPubKey: poolPubKey,
		Messages:   msgToSign,
	}
	blockHeight, err := s.keysignRequestBlockHeight(tasks)
	if err != nil {
		return keysign.Request{}, err
	}
	tssMsg.BlockHeight = blockHeight
	policy, err := s.bridge.GetKeysignPolicy(common.PubKey(tssMsg.PoolPubKey), tssMsg.BlockHeight)
	if err != nil {
		return keysign.Request{}, fmt.Errorf("fail to get keysign policy from thorchain: %w", err)
	}
	// SignerPubKeys is the authoritative active party. NeverSigners and
	// JailedFallbackSigners remain diagnostic metadata for blame and review.
	tssMsg.SignerPubKeys = policy.Signers.Strings()
	tssMsg.NeverSigners = policy.NeverSigners.Strings()
	tssMsg.JailedFallbackSigners = policy.JailedFallbackSigners.Strings()
	tssMsg.RequiredSigners = policy.RequiredSigners
	currentVersion := s.getVersion()
	tssMsg.Version = currentVersion.String()
	return tssMsg, nil
}

func (s *KeySign) keysignRequestBlockHeight(tasks []*tssKeySignTask) (int64, error) {
	if len(tasks) == 0 {
		return 0, fmt.Errorf("no keysign tasks")
	}
	height := tasks[0].Height
	if height <= 0 {
		return 0, fmt.Errorf("keysign task height must be positive")
	}
	for _, task := range tasks[1:] {
		if task.Height != height {
			return 0, fmt.Errorf("mixed keysign task heights: %d and %d", height, task.Height)
		}
	}
	return height, nil
}
