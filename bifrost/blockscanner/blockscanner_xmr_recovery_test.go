package blockscanner_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

type xmrRecoveryBridge struct{ thorclient.ThorchainBridge }

func (xmrRecoveryBridge) GetBlockHeight() (int64, error) { return 1000, nil }
func (xmrRecoveryBridge) GetMimir(string) (int64, error) { return 0, nil }
func (xmrRecoveryBridge) GetLastObservedInHeight(common.Chain) (int64, error) {
	return 1000, nil
}

type xmrRecoveryFetcher struct{ *monero.Scanner }

func (xmrRecoveryFetcher) GetHeight() (int64, error) { return 1000, nil }

type recordingXMRStorage struct {
	blockscanner.ScannerStorage
	writes chan int64
}

func (s *recordingXMRStorage) SetScanPos(height int64) error {
	if err := s.ScannerStorage.SetScanPos(height); err != nil {
		return err
	}
	s.writes <- height
	return nil
}

func TestXMRHeightMismatchStopsStalledScanner(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
	}{
		{name: "wrong height requires operator recovery", status: http.StatusOK},
		{name: "generic error control skips ahead", status: http.StatusInternalServerError},
		{name: "unavailable block keeps retrying", status: http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			retried := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/v1/monero/chain/block/") {
					http.NotFound(w, r)
					return
				}
				if requests.Add(1) == 2 {
					close(retried)
				}
				if test.status != http.StatusOK {
					http.Error(w, "test backend error", test.status)
					return
				}
				_ = json.NewEncoder(w).Encode(sidecar.ChainBlockResp{Height: 102})
			}))
			defer server.Close()
			client, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
			require.NoError(t, err)
			fetcher := xmrRecoveryFetcher{monero.NewScanner(log.Logger, client, nil, common.XMRChain, common.EmptyPubKey, 72)}
			underlying, err := blockscanner.NewBlockScannerStorageWithScanPosSync(t.TempDir(), config.LevelDBOptions{})
			require.NoError(t, err)
			defer underlying.Close()
			require.NoError(t, underlying.SetScanPos(100))
			storage := &recordingXMRStorage{ScannerStorage: underlying, writes: make(chan int64, 1)}
			metric := &metrics.Metrics{}
			errors := metric.GetCounterVec(metrics.CommonBlockScannerError)
			recoveryCounter := errors.WithLabelValues("operator_recovery_required", common.XMRChain.String())
			skipCounter := errors.WithLabelValues("stalled_scanner_skip_ahead", common.XMRChain.String())
			beforeRecovery, beforeSkip := testutil.ToFloat64(recoveryCounter), testutil.ToFloat64(skipCounter)
			loop, err := blockscanner.NewBlockScanner(config.BifrostBlockScannerConfiguration{
				ChainID: common.XMRChain, StartBlockHeight: 100,
				MaxResumeBlockLag: time.Hour, BlockHeightDiscoverBackoff: time.Millisecond,
			}, storage, metric, xmrRecoveryBridge{}, fetcher)
			require.NoError(t, err)
			done := blockscanner.RunStalledScannerForTest(loop)
			stop := sync.OnceFunc(loop.Stop)
			defer stop()

			switch test.status {
			case http.StatusOK:
				select {
				case <-done:
				case height := <-storage.writes:
					t.Fatalf("wrong-height response advanced the durable cursor to %d", height)
				case <-time.After(3 * time.Second):
					t.Fatal("height mismatch did not stop the scanner loop")
				}
				require.Equal(t, int32(1), requests.Load())
				require.Equal(t, beforeRecovery+1, testutil.ToFloat64(recoveryCounter))
				require.Equal(t, beforeSkip, testutil.ToFloat64(skipCounter))
			case http.StatusInternalServerError:
				select {
				case height := <-storage.writes:
					require.Equal(t, int64(970), height, "XMR one-hour lag is 30 blocks below observed height 1000")
				case <-time.After(3 * time.Second):
					t.Fatal("generic-error control did not exercise durable skip-ahead")
				}
				stop()
				<-done
				require.Equal(t, beforeRecovery, testutil.ToFloat64(recoveryCounter))
				require.Equal(t, beforeSkip+1, testutil.ToFloat64(skipCounter))
				require.Equal(t, int64(970), loop.PreviousHeight())
				return
			case http.StatusConflict:
				select {
				case <-retried:
				case <-time.After(3 * time.Second):
					t.Fatal("unavailable block did not retry")
				}
				select {
				case <-done:
					t.Fatal("unavailable block incorrectly stopped for recovery")
				default:
				}
				stop()
				<-done
				require.Equal(t, beforeRecovery, testutil.ToFloat64(recoveryCounter))
				require.Equal(t, beforeSkip, testutil.ToFloat64(skipCounter))
			}
			select {
			case height := <-storage.writes:
				t.Fatalf("unexpected durable cursor write to %d", height)
			default:
			}
			position, positionErr := underlying.GetScanPos()
			require.NoError(t, positionErr)
			require.Equal(t, int64(100), position)
			require.Equal(t, int64(100), loop.PreviousHeight())
			require.False(t, loop.IsHealthy())
		})
	}
}
