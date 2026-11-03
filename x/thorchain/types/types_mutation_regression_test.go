package types

import "testing"

// Deliberately incomplete oracle used to prove the runner reports a survivor.
func TestMutationWeakUnanimity(t *testing.T) {
	if !HasSuperMajority(4, 4) {
		t.Fatal("unanimity must reach quorum")
	}
}

func TestSecurityMutationQuorumBoundary(t *testing.T) {
	for _, tc := range []struct {
		votes, total int
		want         bool
	}{
		{0, 4, false},
		{2, 4, false},
		{3, 4, true},
		{2, 3, true},
		{1, 3, false},
		{7, 10, true},
		{6, 10, false},
		{5, 4, false},
	} {
		if got := HasSuperMajority(tc.votes, tc.total); got != tc.want {
			t.Errorf("HasSuperMajority(%d, %d) = %v, want %v", tc.votes, tc.total, got, tc.want)
		}
	}
}
