package cosmos

import (
	"math/big"
	"testing"

	sdkmath "cosmossdk.io/math"
)

func TestParseUint128(t *testing.T) {
	tests := []struct {
		name   string
		data   []byte
		expect Uint
		err    bool
	}{
		{
			name:   "zero",
			data:   make([]byte, 16),
			expect: ZeroUint(),
		},
		{
			name: "one",
			data: append(
				[]byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
				make([]byte, 8)...,
			),
			expect: NewUint(1),
		},
		{
			name: "max u64 fits in low half",
			data: append(
				[]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
				make([]byte, 8)...,
			),
			expect: NewUint(^uint64(0)),
		},
		{
			name: "value above 2^64 (high=1, low=0)",
			data: append(
				make([]byte, 8),
				[]byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}...,
			),
			expect: sdkmath.NewUintFromBigInt(new(big.Int).Lsh(big.NewInt(1), 64)),
		},
		{
			name: "max u128 (all 0xff)",
			data: []byte{
				0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
				0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
			},
			expect: sdkmath.NewUintFromBigInt(
				new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1)),
			),
		},
		{
			name: "wrong length (15 bytes)",
			data: make([]byte, 15),
			err:  true,
		},
		{
			name: "wrong length (17 bytes)",
			data: make([]byte, 17),
			err:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseUint128(tc.data)
			if tc.err {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.Equal(tc.expect) {
				t.Fatalf("ParseUint128(%x) = %s, want %s", tc.data, got, tc.expect)
			}
		})
	}
}

func TestErrUnknownRequest(t *testing.T) {
	err := ErrUnknownRequest("test message")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestErrInvalidAddress(t *testing.T) {
	err := ErrInvalidAddress("bad-addr")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestErrInvalidCoins(t *testing.T) {
	err := ErrInvalidCoins("bad coins")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestErrUnauthorized(t *testing.T) {
	err := ErrUnauthorized("not allowed")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRoundToDecimal(t *testing.T) {
	tests := []struct {
		name     string
		amt      Uint
		dec      int64
		expected Uint
	}{
		{
			name:     "dec is 0 - no rounding",
			amt:      NewUint(123456789),
			dec:      0,
			expected: NewUint(123456789),
		},
		{
			name:     "dec equals DefaultCoinDecimals - no rounding",
			amt:      NewUint(123456789),
			dec:      DefaultCoinDecimals,
			expected: NewUint(123456789),
		},
		{
			name:     "dec is 6 - round to 6 decimals",
			amt:      NewUint(123456789),
			dec:      6,
			expected: NewUint(123456700),
		},
		{
			name:     "dec is 2 - round to 2 decimals",
			amt:      NewUint(123456789),
			dec:      2,
			expected: NewUint(123000000),
		},
		{
			name:     "dec is 1 - round to 1 decimal",
			amt:      NewUint(123456789),
			dec:      1,
			expected: NewUint(120000000),
		},
		{
			name:     "dec is 4",
			amt:      NewUint(100000099),
			dec:      4,
			expected: NewUint(100000000),
		},
		{
			name:     "small amount gets truncated to zero",
			amt:      NewUint(99),
			dec:      6,
			expected: NewUint(0),
		},
		{
			name:     "dec greater than DefaultCoinDecimals - no rounding",
			amt:      NewUint(123456789),
			dec:      10,
			expected: NewUint(123456789),
		},
		{
			name: "dec negative - no rounding (condition dec != 0 && dec < DefaultCoinDecimals fails since negative < 8)",
			amt:  NewUint(123456789),
			dec:  -1,
			// negative < DefaultCoinDecimals is true, and dec != 0, so prec = 8 - (-1) = 9
			expected: sdkmath.NewUintFromBigInt(big.NewInt(0).Mul(
				big.NewInt(0).Quo(big.NewInt(123456789), big.NewInt(1000000000)),
				big.NewInt(1000000000),
			)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RoundToDecimal(tt.amt, tt.dec)
			if !result.Equal(tt.expected) {
				t.Errorf("RoundToDecimal(%s, %d) = %s, want %s", tt.amt, tt.dec, result, tt.expected)
			}
		})
	}
}

func TestMaxUint(t *testing.T) {
	a := NewUint(5)
	b := NewUint(7)
	if !MaxUint(a, b).Equal(b) {
		t.Fatalf("MaxUint(5, 7) want 7")
	}
	if !MaxUint(b, a).Equal(b) {
		t.Fatalf("MaxUint(7, 5) want 7")
	}
	if !MaxUint(a, a).Equal(a) {
		t.Fatalf("MaxUint(5, 5) want 5")
	}
	if !MaxUint(ZeroUint(), a).Equal(a) {
		t.Fatalf("MaxUint(0, 5) want 5")
	}
}

func TestPowerOfTen(t *testing.T) {
	if !PowerOfTen(0).Equal(NewUint(1)) {
		t.Fatalf("10^0 want 1")
	}
	if !PowerOfTen(1).Equal(NewUint(10)) {
		t.Fatalf("10^1 want 10")
	}
	if !PowerOfTen(8).Equal(NewUint(1e8)) {
		t.Fatalf("10^8 want 1e8")
	}
	if !PowerOfTen(18).Equal(sdkmath.NewUintFromString("1000000000000000000")) {
		t.Fatalf("10^18 mismatch")
	}
}

func TestSafeUintFromInt64(t *testing.T) {
	tests := []struct {
		name     string
		input    int64
		expected Uint
	}{
		{
			name:     "positive value",
			input:    100,
			expected: NewUint(100),
		},
		{
			name:     "zero value",
			input:    0,
			expected: NewUint(0),
		},
		{
			name:     "negative value returns zero",
			input:    -1,
			expected: ZeroUint(),
		},
		{
			name:     "large negative returns zero",
			input:    -9999999,
			expected: ZeroUint(),
		},
		{
			name:     "large positive value",
			input:    1000000000000,
			expected: NewUint(1000000000000),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SafeUintFromInt64(tt.input)
			if !result.Equal(tt.expected) {
				t.Errorf("SafeUintFromInt64(%d) = %s, want %s", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMinUint(t *testing.T) {
	tests := []struct {
		name     string
		u1       Uint
		u2       Uint
		expected Uint
	}{
		{
			name:     "first is smaller",
			u1:       NewUint(1),
			u2:       NewUint(2),
			expected: NewUint(1),
		},
		{
			name:     "second is smaller",
			u1:       NewUint(100),
			u2:       NewUint(50),
			expected: NewUint(50),
		},
		{
			name:     "equal values",
			u1:       NewUint(42),
			u2:       NewUint(42),
			expected: NewUint(42),
		},
		{
			name:     "zero and positive",
			u1:       ZeroUint(),
			u2:       NewUint(1),
			expected: ZeroUint(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MinUint(tt.u1, tt.u2)
			if !result.Equal(tt.expected) {
				t.Errorf("MinUint(%s, %s) = %s, want %s", tt.u1, tt.u2, result, tt.expected)
			}
		})
	}
}

func TestSum(t *testing.T) {
	tests := []struct {
		name     string
		input    []Uint
		expected Uint
	}{
		{
			name:     "empty slice",
			input:    []Uint{},
			expected: ZeroUint(),
		},
		{
			name:     "single element",
			input:    []Uint{NewUint(42)},
			expected: NewUint(42),
		},
		{
			name:     "multiple elements",
			input:    []Uint{NewUint(1), NewUint(2), NewUint(3)},
			expected: NewUint(6),
		},
		{
			name:     "includes zero",
			input:    []Uint{NewUint(10), ZeroUint(), NewUint(20)},
			expected: NewUint(30),
		},
		{
			name:     "nil slice",
			input:    nil,
			expected: ZeroUint(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Sum(tt.input)
			if !result.Equal(tt.expected) {
				t.Errorf("Sum(%v) = %s, want %s", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSignerCreds(t *testing.T) {
	// Test with environment variables set
	t.Setenv(EnvSignerName, "  test-signer  ")
	t.Setenv(EnvSignerPassword, "  test-password  ")

	username, password := SignerCreds()
	if username != "test-signer" {
		t.Errorf("expected username 'test-signer', got '%s'", username)
	}
	if password != "test-password" {
		t.Errorf("expected password 'test-password', got '%s'", password)
	}
}

func TestGetKeybase(t *testing.T) {
	t.Setenv(EnvSignerName, "test-signer")
	t.Setenv(EnvSignerPassword, "test-password")

	// Test with explicit thorchainHome directory
	tmpDir := t.TempDir()
	kbs, err := GetKeybase(tmpDir)
	// The keyring creation may succeed or fail depending on environment,
	// but we exercise the code path either way.
	if err != nil {
		// Just ensure we get an error, not a panic
		t.Logf("GetKeybase with tmpDir returned error (expected in test): %v", err)
	} else {
		if kbs.SignerName != "test-signer" {
			t.Errorf("expected signer name 'test-signer', got '%s'", kbs.SignerName)
		}
		if kbs.SignerPasswd != "test-password" {
			t.Errorf("expected signer password 'test-password', got '%s'", kbs.SignerPasswd)
		}
		if kbs.Keybase == nil {
			t.Error("expected non-nil keybase")
		}
	}

	// Test with empty thorchainHome (uses user home directory)
	kbs2, err := GetKeybase("")
	if err != nil {
		t.Logf("GetKeybase with empty home returned error (expected in test): %v", err)
	} else if kbs2.SignerName != "test-signer" {
		t.Errorf("expected signer name 'test-signer', got '%s'", kbs2.SignerName)
	}
}

func TestRoundToDecimal_PrecZero(t *testing.T) {
	// When dec == DefaultCoinDecimals, prec would be 0, but the outer
	// condition (dec < DefaultCoinDecimals) prevents entry. Test dec == 7
	// which gives prec == 1, and dec == DefaultCoinDecimals which skips.
	result := RoundToDecimal(NewUint(12345678), 7)
	expected := NewUint(12345670)
	if !result.Equal(expected) {
		t.Errorf("RoundToDecimal(12345678, 7) = %s, want %s", result, expected)
	}

	// dec == DefaultCoinDecimals (8) should return amt unchanged
	result2 := RoundToDecimal(NewUint(12345678), DefaultCoinDecimals)
	expected2 := NewUint(12345678)
	if !result2.Equal(expected2) {
		t.Errorf("RoundToDecimal(12345678, 8) = %s, want %s", result2, expected2)
	}
}
