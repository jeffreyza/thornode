package core

import (
	"fmt"
	rand "math/rand/v2"
	"sort"
	"time"

	"github.com/rs/zerolog"

	atypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	tronSolvencySimulationMinimumValidatorCount = 4
	tronSolvencySlashPointAttempts              = 5
	tronUSDTAssetString                         = "TRON.USDT-TR7NHQJEKQXGTCI8Q8ZY4PL8OTSZGJLJ6T"
)

// TRONSolvencyFragmentationActor exercises #3098 through a running cluster
// with at least four active validators. Two different TRON balance payloads use the same canonical report
// height: one validator signs the minority payload and two sign the other
// payload. Ordinary solvency observations must not change slash points, while
// replaying an identical direct MsgSolvency must retain its anti-spam penalty.
//
// This intentionally injects the two payloads at the thornode boundary. It
// verifies the consensus/slashing policy without requiring divergent Java-Tron
// nodes, and does not claim that Bifrost can obtain historical TRON balances.
type TRONSolvencyFragmentationActor struct {
	Actor
}

func NewTRONSolvencyFragmentationActor(rng *rand.Rand) *Actor {
	a := &TRONSolvencyFragmentationActor{
		Actor: *NewActor("TRONSolvencyFragmentation", rng),
	}
	a.Timeout = 5 * time.Minute
	a.Ops = append(a.Ops, a.run)
	return &a.Actor
}

func (a *TRONSolvencyFragmentationActor) run(config *OpConfig) OpResult {
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Info().Err(err).Msg("waiting for node accounts")
		return OpResult{Continue: false}
	}

	users, addresses, err := activeTRONSolvencyUsers(config, nodes)
	if err != nil {
		return OpResult{Finish: true, Error: err}
	}

	release, acquired := acquireTRONSolvencyUsers(users)
	if !acquired {
		a.Log().Info().Msg("waiting for exclusive access to simulation validator clients")
		return OpResult{Continue: false}
	}
	defer release()

	_, vaultPubKey, err := getActiveVaultForChain(common.TRONChain)
	if err != nil {
		a.Log().Info().Err(err).Msg("waiting for active TRON vault")
		return OpResult{Continue: false}
	}

	lastChainHeight, err := users[0].Thorchain.GetLastObservedInHeight(common.TRONChain)
	if err != nil {
		a.Log().Info().Err(err).Msg("waiting for TRON last chain height")
		return OpResult{Continue: false}
	}
	reportHeight, ok := previousTRONSolvencyReportHeight(lastChainHeight)
	if !ok {
		a.Log().Info().Int64("last_chain_height", lastChainHeight).Msg("waiting for a prior canonical TRON report height")
		return OpResult{Continue: false}
	}

	thorHeight, err := users[0].Thorchain.GetBlockHeight()
	if err != nil {
		a.Log().Info().Err(err).Msg("waiting for THORChain block height")
		return OpResult{Continue: false}
	}

	constantValues, err := users[0].Thorchain.GetConstants()
	if err != nil {
		a.Log().Info().Err(err).Msg("waiting for thornode constants")
		return OpResult{Continue: false}
	}
	observeSlashPoints, ok := constantValues[constants.ObserveSlashPoints.String()]
	if !ok || observeSlashPoints <= 0 {
		return OpResult{Finish: true, Error: fmt.Errorf("invalid %s constant: %d", constants.ObserveSlashPoints, observeSlashPoints)}
	}

	// From this point onward, do not retry the operation: replaying an already
	// delivered message would turn it into the duplicate case being tested.
	// Bifrost observations keep changing slash points in the background, so a
	// mismatch is measured again with a fresh seed, which creates new voters.
	var submissions []tronSolvencySubmission
	err = retryTRONSolvencySlashPoints(a.Log(), "observations", nil, func(attempt int) (map[string]int64, map[string]int64, error) {
		submissions, err = tronSolvencyFragmentationSubmissions(users, vaultPubKey, reportHeight, uint64(thorHeight)+uint64(attempt))
		if err != nil {
			return nil, nil, err
		}
		return a.measureTRONSolvencySlashPoints(addresses, submissions)
	})
	if err != nil {
		return OpResult{Finish: true, Error: fmt.Errorf("ordinary fragmented TRON solvency observations: %w", err)}
	}

	// Each replay of the delivered majority message is another duplicate.
	duplicate := []tronSolvencySubmission{{submissions[1].user, submissions[1].msg, "duplicate"}}
	err = retryTRONSolvencySlashPoints(a.Log(), "duplicate", map[string]int64{
		addresses[1]: observeSlashPoints,
	}, func(int) (map[string]int64, map[string]int64, error) {
		return a.measureTRONSolvencySlashPoints(addresses, duplicate)
	})
	if err != nil {
		return OpResult{Finish: true, Error: fmt.Errorf("duplicate TRON solvency penalty: %w", err)}
	}

	a.Log().Info().
		Int64("report_height", reportHeight).
		Int64("last_chain_height", lastChainHeight).
		Stringer("minority_id", submissions[0].msg.Id).
		Stringer("majority_id", submissions[1].msg.Id).
		Msg("verified TRON solvency fragmentation slashing policy")
	return OpResult{Continue: true}
}

type tronSolvencySubmission struct {
	user *User
	msg  *types.MsgSolvency
	name string
}

// tronSolvencyFragmentationSubmissions builds one minority and two identical
// majority observations for the same report height. Each seed yields new voter
// ids, so a retry with a different seed does not replay an earlier message.
func tronSolvencyFragmentationSubmissions(users []*User, vaultPubKey common.PubKey, reportHeight int64, seed uint64) ([]tronSolvencySubmission, error) {
	majorityCoins, minorityCoins, err := tronSolvencyFragmentationPayloads(seed)
	if err != nil {
		return nil, err
	}

	minorityMsg := users[0].Thorchain.GetSolvencyMsg(reportHeight, common.TRONChain, vaultPubKey, minorityCoins)
	majorityMsg1 := users[1].Thorchain.GetSolvencyMsg(reportHeight, common.TRONChain, vaultPubKey, majorityCoins)
	majorityMsg2 := users[2].Thorchain.GetSolvencyMsg(reportHeight, common.TRONChain, vaultPubKey, majorityCoins)
	if minorityMsg == nil || majorityMsg1 == nil || majorityMsg2 == nil {
		return nil, fmt.Errorf("failed to build TRON solvency messages")
	}
	if minorityMsg.Id.Equals(majorityMsg1.Id) {
		return nil, fmt.Errorf("fragmented TRON payloads produced the same voter id")
	}
	if !majorityMsg1.Id.Equals(majorityMsg2.Id) {
		return nil, fmt.Errorf("identical TRON payloads produced different voter ids")
	}
	return []tronSolvencySubmission{
		{users[0], minorityMsg, "minority"},
		{users[1], majorityMsg1, "majority-1"},
		{users[2], majorityMsg2, "majority-2"},
	}, nil
}

// measureTRONSolvencySlashPoints broadcasts the submissions in order and sums
// slash points before and after each distinct block that delivered one, so the
// delta excludes updates in blocks between or around the submissions.
func (a *TRONSolvencyFragmentationActor) measureTRONSolvencySlashPoints(addresses []string, submissions []tronSolvencySubmission) (map[string]int64, map[string]int64, error) {
	before := make(map[string]int64, len(addresses))
	after := make(map[string]int64, len(addresses))
	measured := make(map[int64]bool, len(submissions))
	for _, submission := range submissions {
		txID, height, err := broadcastAndConfirmTRONSolvency(submission.user, submission.msg)
		if err != nil {
			return nil, nil, fmt.Errorf("broadcast %s TRON solvency payload: %w", submission.name, err)
		}
		a.Log().Info().Str("submission", submission.name).Stringer("txid", txID).Int64("height", height).Msg("broadcasted TRON solvency payload")
		if measured[height] {
			continue
		}
		measured[height] = true
		if err = addSlashPointsAtHeight(before, height-1, addresses); err != nil {
			return nil, nil, err
		}
		if err = addSlashPointsAtHeight(after, height, addresses); err != nil {
			return nil, nil, err
		}
	}
	return before, after, nil
}

// retryTRONSolvencySlashPoints compares the slash-point deltas of each
// measurement with expected. Unrelated observations in the same blocks can
// cause a mismatch, so it measures again; a real regression mismatches on every
// attempt. Measurement errors are returned without retrying.
func retryTRONSolvencySlashPoints(log *zerolog.Logger, check string, expected map[string]int64, measure func(attempt int) (map[string]int64, map[string]int64, error)) error {
	var mismatch error
	for attempt := 1; attempt <= tronSolvencySlashPointAttempts; attempt++ {
		before, after, err := measure(attempt)
		if err != nil {
			return err
		}
		if mismatch = validateSlashPointDeltas(before, after, expected); mismatch == nil {
			return nil
		}
		log.Warn().Err(mismatch).Str("check", check).Int("attempt", attempt).Msg("TRON solvency slash-point delta mismatch, retrying")
	}
	return fmt.Errorf("slash-point delta mismatched on all %d attempts: %w", tronSolvencySlashPointAttempts, mismatch)
}

// broadcastAndConfirmTRONSolvency returns the txid and the THORChain height of
// the block that delivered it.
func broadcastAndConfirmTRONSolvency(user *User, msg cosmos.Msg) (common.TxID, int64, error) {
	txID, err := user.Thorchain.Broadcast(msg)
	if err != nil {
		return txID, 0, err
	}

	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for {
		var response struct {
			TxResponse *struct {
				Height int64  `json:"height,string"`
				Code   uint32 `json:"code"`
				RawLog string `json:"raw_log"`
			} `json:"tx_response"`
		}
		url := fmt.Sprintf("%s/cosmos/tx/v1beta1/txs/%s", thornode.BaseURL(), txID)
		lastErr = thornode.Get(url, &response)
		switch {
		case lastErr != nil:
		case response.TxResponse == nil:
			lastErr = fmt.Errorf("transaction response is missing")
		case response.TxResponse.Code != 0:
			return txID, 0, fmt.Errorf("DeliverTx code %d: %s", response.TxResponse.Code, response.TxResponse.RawLog)
		default:
			return txID, response.TxResponse.Height, nil
		}
		if time.Now().After(deadline) {
			return txID, 0, fmt.Errorf("transaction %s was not confirmed: %w", txID, lastErr)
		}
		time.Sleep(time.Second)
	}
}

func activeTRONSolvencyUsers(config *OpConfig, nodes []openapi.Node) ([]*User, []string, error) {
	// Activation belongs to a preceding stage, not this five-minute assertion
	// actor. Fail before acquiring clients or broadcasting if it was omitted.
	activeNodes := activeValidatorCount(nodes)
	if activeNodes < tronSolvencySimulationMinimumValidatorCount || len(config.NodeUsers) < activeNodes {
		return nil, nil, fmt.Errorf("TRON solvency setup requires at least %d active validators and matching simulation users: got %d active validators and %d simulation users; run make test-simulation-cluster TEST_SIMULATION_CLUSTER_STAGES=seed,bootstrap,churn,tron-solvency-fragmentation, or complete the churn stage before tron-solvency-fragmentation", tronSolvencySimulationMinimumValidatorCount, activeNodes, len(config.NodeUsers))
	}

	// Extra clients may belong to standby validators. Track each active address
	// and whether its client has been selected, preserving configured user order.
	activeAddresses := make(map[string]bool, activeNodes)
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() {
			activeAddresses[node.NodeAddress] = false
		}
	}

	users := make([]*User, 0, activeNodes)
	addresses := make([]string, 0, activeNodes)
	for _, user := range config.NodeUsers {
		if user == nil {
			return nil, nil, fmt.Errorf("simulation node user is nil")
		}
		address, err := user.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			return nil, nil, fmt.Errorf("derive simulation node address: %w", err)
		}
		selected, active := activeAddresses[address.String()]
		if !active {
			continue
		}
		if selected {
			return nil, nil, fmt.Errorf("TRON solvency setup: duplicate simulation user for active validator %s", address)
		}
		activeAddresses[address.String()] = true
		users = append(users, user)
		addresses = append(addresses, address.String())
	}
	if len(users) != activeNodes {
		return nil, nil, fmt.Errorf("TRON solvency setup: missing simulation users for active validators: matched %d of %d", len(users), activeNodes)
	}
	return users, addresses, nil
}

func acquireTRONSolvencyUsers(users []*User) (func(), bool) {
	acquired := make([]*User, 0, len(users))
	for _, user := range users {
		if !user.Acquire() {
			for i := len(acquired) - 1; i >= 0; i-- {
				acquired[i].Release()
			}
			return func() {}, false
		}
		acquired = append(acquired, user)
	}
	return func() {
		for i := len(acquired) - 1; i >= 0; i-- {
			acquired[i].Release()
		}
	}, true
}

// previousTRONSolvencyReportHeight chooses the preceding ten-block report
// bucket. It is canonical and strictly below keeper last-chain-height, so the
// synthetic consensus cannot halt or unhalt TRON during the simulation.
func previousTRONSolvencyReportHeight(lastChainHeight int64) (int64, bool) {
	height := lastChainHeight - lastChainHeight%10 - 10
	return height, height > 0
}

func tronSolvencyFragmentationPayloads(seed uint64) (common.Coins, common.Coins, error) {
	usdt, err := common.NewAsset(tronUSDTAssetString)
	if err != nil {
		return nil, nil, fmt.Errorf("parse TRON USDT asset: %w", err)
	}

	majority := common.NewCoins(
		common.NewCoin(common.TRXAsset, cosmos.NewUint(1_000_000_000+seed)),
		common.NewCoin(usdt, cosmos.NewUint(10_000_000_000+seed)),
	)
	minority := majority.Copy()
	minorityUSDT := minority.GetCoin(usdt)
	minorityUSDT.Amount = minorityUSDT.Amount.Sub(cosmos.OneUint())
	minority = common.NewCoins(minority.GetCoin(common.TRXAsset), minorityUSDT)
	return majority, minority, nil
}

func addSlashPointsAtHeight(sum map[string]int64, height int64, addresses []string) error {
	nodes, err := thornode.GetNodesAtHeight(height)
	if err != nil {
		return fmt.Errorf("query slash points at height %d: %w", height, err)
	}
	points, err := slashPointsForAddresses(nodes, addresses)
	if err != nil {
		return err
	}
	for address, value := range points {
		sum[address] += value
	}
	return nil
}

func slashPointsForAddresses(nodes []openapi.Node, addresses []string) (map[string]int64, error) {
	pointsByAddress := make(map[string]int64, len(nodes))
	for _, node := range nodes {
		pointsByAddress[node.NodeAddress] = node.SlashPoints
	}

	result := make(map[string]int64, len(addresses))
	for _, address := range addresses {
		points, ok := pointsByAddress[address]
		if !ok {
			return nil, fmt.Errorf("node account %s not found", address)
		}
		result[address] = points
	}
	return result, nil
}

func validateSlashPointDeltas(before, after, expected map[string]int64) error {
	addresses := make([]string, 0, len(before))
	for address := range before {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)

	for _, address := range addresses {
		afterPoints, ok := after[address]
		if !ok {
			return fmt.Errorf("node account %s missing from after snapshot", address)
		}
		want := expected[address]
		if got := afterPoints - before[address]; got != want {
			return fmt.Errorf("node %s slash-point delta: got %d, want %d (before %d, after %d)", address, got, want, before[address], afterPoints)
		}
	}
	return nil
}
