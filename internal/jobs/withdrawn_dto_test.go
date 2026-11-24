package jobs

import "testing"

func TestJobWithdrawnAtClaim(t *testing.T) {
	tests := []struct {
		name string
		job  Job
		want bool
	}{
		{"withdrawn at first claim", Job{Status: StatusError, Attempts: 1, Error: WithdrawnAtClaimError}, true},
		{"ordinary error", Job{Status: StatusError, Attempts: 1, Error: "boom"}, false},
		{"refused on a later claim", Job{Status: StatusError, Attempts: 2, Error: WithdrawnAtClaimError}, false},
		{"not an error status", Job{Status: StatusFailed, Attempts: 1, Error: WithdrawnAtClaimError}, false},
	}
	for _, tc := range tests {
		if got := tc.job.WithdrawnAtClaim(); got != tc.want {
			t.Errorf("%s: WithdrawnAtClaim = %v, want %v", tc.name, got, tc.want)
		}
	}
}
