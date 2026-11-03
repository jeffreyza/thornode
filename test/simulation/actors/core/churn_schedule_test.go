package core

import "testing"

func TestScheduledChurnIntervalLandsLeadBlocksAhead(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		current, last, lead    int64
		wantInterval, wantFire int64
	}{
		// first cluster churn: genesis vault, well past the 60-block regular height
		{"genesis vault", 916, 0, 20, 936, 936},
		// churn-out: previous churn at 1193, its migration finished at 1300
		{"after migration", 1300, 1193, 20, 127, 1320},
		// a vault newer than the current height never schedules into the past
		{"fresh vault", 10, 12, 20, 20, 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			interval := scheduledChurnInterval(tc.current, tc.last, tc.lead)
			if interval != tc.wantInterval {
				t.Fatalf("interval: got %d, want %d", interval, tc.wantInterval)
			}
			// manager_validator_current.go fires a regular churn when
			// lastChurnHeight+ChurnInterval == height
			if fire := tc.last + interval; fire != tc.wantFire {
				t.Fatalf("churn height: got %d, want %d", fire, tc.wantFire)
			}
			if tc.last+interval < tc.current+tc.lead && tc.current > tc.last {
				t.Fatalf("churn at %d is less than %d blocks after %d", tc.last+interval, tc.lead, tc.current)
			}
		})
	}
}
