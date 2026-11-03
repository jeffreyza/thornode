package keeperv1

import "testing"

func TestGetSlashCallerFromStack(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		fn    string
		stack string
		want  string
	}{
		{
			name: "slasher increment wrapper retains existing attribution",
			fn:   "IncSlashPoints",
			stack: "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1.KVStore.IncNodeAccountSlashPoints(...)\n" +
				"\t/keeper_node_account.go:391\n" +
				"gitlab.com/thorchain/thornode/v3/x/thorchain.(*SlasherImpl).IncSlashPoints(...)\n" +
				"\t/manager_slasher_current.go:1078\n" +
				"gitlab.com/thorchain/thornode/v3/x/thorchain.observeFailure(...)\n" +
				"\t/handler.go:42\n",
			want: "/handler.go:42",
		},
		{
			name: "direct keeper increment falls back to its concrete frame",
			fn:   "IncSlashPoints",
			stack: "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1.KVStore.IncNodeAccountSlashPoints(...)\n" +
				"\t/keeper_node_account.go:391\n" +
				"gitlab.com/thorchain/thornode/v3/x/thorchain.TssKeysignHandler.handle(...)\n" +
				"\t/handler_tss_keysign.go:123\n",
			want: "/handler_tss_keysign.go:123",
		},
		{
			name: "slasher decrement wrapper retains existing attribution",
			fn:   "DecSlashPoints",
			stack: "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1.KVStore.DecNodeAccountSlashPoints(...)\n" +
				"\t/keeper_node_account.go:422\n" +
				"gitlab.com/thorchain/thornode/v3/x/thorchain.(*SlasherImpl).DecSlashPoints(...)\n" +
				"\t/manager_slasher_current.go:1087\n" +
				"gitlab.com/thorchain/thornode/v3/x/thorchain.refundObservation(...)\n" +
				"\t/handler.go:84\n",
			want: "/handler.go:84",
		},
		{
			name: "direct keeper decrement falls back to its concrete frame",
			fn:   "DecSlashPoints",
			stack: "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1.KVStore.DecNodeAccountSlashPoints(...)\n" +
				"\t/keeper_node_account.go:422\n" +
				"gitlab.com/thorchain/thornode/v3/x/thorchain.TssKeysignHandler.handle(...)\n" +
				"\t/handler_tss_keysign.go:307\n",
			want: "/handler_tss_keysign.go:307",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := getSlashCallerFromStack([]byte(test.stack), test.fn); got != test.want {
				t.Fatalf("getSlashCallerFromStack() = %q, want %q", got, test.want)
			}
		})
	}
}
