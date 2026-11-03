package ss58

import (
	"encoding/hex"
	"testing"
)

func TestEncodeDecode(t *testing.T) {
	// Test vector: a well-known SS58 address with prefix 42
	// The all-zero 32-byte pubkey with prefix 42 should produce a deterministic address
	pubkey := make([]byte, 32)
	for i := range pubkey {
		pubkey[i] = byte(i)
	}

	// Encode
	addr, err := Encode(42, pubkey)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	// Should start with "5" for prefix 42
	if addr[0] != '5' {
		t.Errorf("expected address to start with '5', got '%c'", addr[0])
	}

	// Decode and verify
	prefix, decoded, err := Decode(addr)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if prefix != 42 {
		t.Errorf("expected prefix 42, got %d", prefix)
	}

	if hex.EncodeToString(decoded) != hex.EncodeToString(pubkey) {
		t.Errorf("decoded pubkey doesn't match original")
	}
}

func TestEncodeInvalidPubkeyLength(t *testing.T) {
	_, err := Encode(42, []byte{1, 2, 3})
	if err == nil {
		t.Error("expected error for invalid pubkey length")
	}
}

func TestDecodeInvalidBase58(t *testing.T) {
	_, _, err := Decode("invalid-not-base58-0OIl")
	if err == nil {
		t.Error("expected error for invalid base58")
	}
}

func TestDecodeInvalidChecksum(t *testing.T) {
	// Create a valid encoding then corrupt the checksum
	pubkey := make([]byte, 32)
	addr, err := Encode(42, pubkey)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	// Corrupt the last character
	corrupted := addr[:len(addr)-1] + "x"
	_, _, err = Decode(corrupted)
	// This should fail with either invalid base58 or invalid checksum
	if err == nil {
		t.Error("expected error for corrupted checksum")
	}
}

func TestKnownSS58Address(t *testing.T) {
	// Known test vector: Alice's well-known dev account
	// Pubkey: 0xd43593c715fdd31c61141abd04a99fd6822c8558854ccde39a5684e7a56da27d
	// SS58 (prefix 42): 5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY
	pubkeyHex := "d43593c715fdd31c61141abd04a99fd6822c8558854ccde39a5684e7a56da27d"
	pubkey, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		t.Fatalf("failed to decode pubkey hex: %v", err)
	}

	addr, err := Encode(42, pubkey)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	expected := "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY"
	if addr != expected {
		t.Errorf("expected %s, got %s", expected, addr)
	}

	// Decode it back
	prefix, decodedPubkey, err := Decode(addr)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if prefix != 42 {
		t.Errorf("expected prefix 42, got %d", prefix)
	}

	if hex.EncodeToString(decodedPubkey) != pubkeyHex {
		t.Errorf("decoded pubkey doesn't match: expected %s, got %s", pubkeyHex, hex.EncodeToString(decodedPubkey))
	}
}

func TestKnownBobAddress(t *testing.T) {
	// Bob's well-known dev account
	// Pubkey: 0x8eaf04151687736326c9fea17e25fc5287613693c912909cb226aa4794f26a48
	// SS58 (prefix 42): 5FHneW46xGXgs5mUiveU4sbTyGBzmstUspZC92UhjJM694ty
	pubkeyHex := "8eaf04151687736326c9fea17e25fc5287613693c912909cb226aa4794f26a48"
	pubkey, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		t.Fatalf("failed to decode pubkey hex: %v", err)
	}

	addr, err := Encode(42, pubkey)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	expected := "5FHneW46xGXgs5mUiveU4sbTyGBzmstUspZC92UhjJM694ty"
	if addr != expected {
		t.Errorf("expected %s, got %s", expected, addr)
	}

	prefix, decoded, err := Decode(addr)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if prefix != 42 {
		t.Errorf("expected prefix 42, got %d", prefix)
	}
	if hex.EncodeToString(decoded) != pubkeyHex {
		t.Errorf("decoded pubkey mismatch")
	}
}

func TestPolkadotPrefix(t *testing.T) {
	// Alice's pubkey with Polkadot prefix 0 should produce a different address starting with "1"
	pubkeyHex := "d43593c715fdd31c61141abd04a99fd6822c8558854ccde39a5684e7a56da27d"
	pubkey, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		t.Fatalf("failed to decode pubkey hex: %v", err)
	}

	addr, err := Encode(0, pubkey)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	if addr[0] != '1' {
		t.Errorf("Polkadot addresses should start with '1', got '%c'", addr[0])
	}

	prefix, decoded, err := Decode(addr)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if prefix != 0 {
		t.Errorf("expected prefix 0, got %d", prefix)
	}
	if hex.EncodeToString(decoded) != pubkeyHex {
		t.Errorf("decoded pubkey mismatch")
	}
}

func TestDecodeInvalidLength(t *testing.T) {
	// A valid base58 string that decodes to wrong length (not 35 bytes)
	// base58 encode of a 10-byte value
	_, _, err := Decode("2DrSFEnzB") // short base58
	if err == nil {
		t.Error("expected error for invalid length")
	}
}

func TestAllZeroPubkey(t *testing.T) {
	pubkey := make([]byte, 32)
	addr, err := Encode(42, pubkey)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	prefix, decoded, err := Decode(addr)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if prefix != 42 {
		t.Errorf("expected prefix 42, got %d", prefix)
	}
	for i, b := range decoded {
		if b != 0 {
			t.Errorf("decoded byte %d: got %d, want 0", i, b)
		}
	}
}
