package api

import (
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

func TestJobDTOsCarryWithdrawnAtClaim(t *testing.T) {
	withdrawn := jobs.Job{Status: jobs.StatusError, Attempts: 1, Error: jobs.WithdrawnAtClaimError}
	failed := jobs.Job{Status: jobs.StatusError, Attempts: 1, Error: "boom"}
	if !toJobDTO(withdrawn).WithdrawnAtClaim || toJobDTO(failed).WithdrawnAtClaim {
		t.Error("toJobDTO: withdrawn_at_claim wrong")
	}
	if !toJobDetailDTO(withdrawn, nil).WithdrawnAtClaim || toJobDetailDTO(failed, nil).WithdrawnAtClaim {
		t.Error("toJobDetailDTO: withdrawn_at_claim wrong")
	}
	withdrawn.ID, failed.ID = uuid.New(), uuid.New()
	d := toJobDetailDTO(failed, []jobs.Job{withdrawn})
	if len(d.Group) != 1 || !d.Group[0].WithdrawnAtClaim {
		t.Errorf("group member: %+v", d.Group)
	}
}
