package common

import "errors"

const CompromisedVaultErrorText = "marked compromised"

var ErrCompromisedVault = errors.New(CompromisedVaultErrorText)
