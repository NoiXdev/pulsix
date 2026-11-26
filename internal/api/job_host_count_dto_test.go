package api

import (
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// The runs list says how many hosts each job targets (backlog 1.53: the
// template page's Runs tab), without the host ids themselves.
func TestJobDTOCarriesHostCount(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hosts []uuid.UUID
		want  int
	}{
		{"none", nil, 0},
		{"one", []uuid.UUID{uuid.New()}, 1},
		{"three", []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := toJobDTO(jobs.Job{HostIDs: tc.hosts}).HostCount; got != tc.want {
				t.Errorf("host_count = %d, want %d", got, tc.want)
			}
		})
	}
}
