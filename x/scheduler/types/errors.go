package types

// DONTCOVER

import (
	"cosmossdk.io/errors"
)

// x/denom module sentinel errors
var (
	ErrInvalidGenesis     = errors.Register(ModuleName, 1, "invalid genesis")
	ErrInvalidHeight      = errors.Register(ModuleName, 2, "invalid height")
	ErrScheduleHeightFull = errors.Register(ModuleName, 3, "schedule for target height is full")
	ErrAfterTooLarge      = errors.Register(ModuleName, 4, "after exceeds maximum")
)
