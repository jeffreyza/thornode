package types

import "cosmossdk.io/collections"

const (
	// ModuleName defines the module name
	ModuleName = "scheduler"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// QuerierRoute defines the module's query routing key
	QuerierRoute = ModuleName

	// MaxMsgsPerHeight and MaxScheduleExecGas together bound total per-block
	// scheduled work to MaxMsgsPerHeight × MaxScheduleExecGas, since
	// ExecuteSchedule runs only the current height's queue in EndBlock.
	//
	// ESTIMATE — not yet measured. These are placeholders pending a benchmark of
	// worst-case scheduled-wasm wall-clock per gas on validator-grade hardware.
	// Derivation: THORChain runs with no block gas limit (CometBFT default
	// max_gas = -1), so the only ceiling on EndBlock work is wall-clock against
	// the 6s block time. Budgeting the scheduler a small slice (~200ms) at the
	// rule-of-thumb ~100M SDK gas/s gives a target product of ~20M SDK gas:
	//   10 × 2_000_000 = 20M  (≈0.2s worst case; was 100 × 30M = 3,000M ≈ 30s).
	// Both are consensus parameters and must ship in a coordinated upgrade; tune
	// down further if the benchmark shows worse-than-assumed throughput.

	// MaxMsgsPerHeight bounds how many scheduled executions may target a single
	// height, directly bounding per-block scheduled-execution count.
	MaxMsgsPerHeight = 10

	// MaxAfter bounds how far in the future a message may be scheduled. With
	// MaxMsgsPerHeight this bounds total pending storage.
	MaxAfter = 100_000

	// MaxScheduleExecGas bounds the gas a single scheduled wasm execution may
	// consume. EndBlock runs under an infinite gas meter, so without this a
	// single contract execution has unbounded compute.
	MaxScheduleExecGas = 2_000_000
)

var (
	ScheduleKey    = "schedule"
	SchedulePrefix = collections.NewPrefix(0)

	SenderIndexKey    = "sender_index"
	SenderIndexPrefix = collections.NewPrefix(1)
)
