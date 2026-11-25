// This file is the write side of backlog 4.42: a tuple the evaluation
// stops measuring keeps its check_states row, marked, so that it starts
// fresh when it is measured again (TupleState.UnmeasuredAt).
package rules

import (
	"context"
	"fmt"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// markUnmeasured stamps the rows of the tuples res reports unmeasured with
// the evaluation's moment (backlog 4.42): the rows stand, and the stamp is
// what makes the tuple start fresh when it is measured again
// (TupleState.UnmeasuredAt). The statement itself keeps the first stamp of
// a gap; an evaluation with nothing unmeasured costs no statement.
func markUnmeasured(ctx context.Context, q *gen.Queries, d Due, res Result) error {
	if len(res.Unmeasured) == 0 {
		return nil
	}
	arg := gen.MarkCheckStatesUnmeasuredParams{
		At: res.At, OrgID: d.OrgID, HostID: d.HostID,
		CheckKeys: make([]string, len(res.Unmeasured)),
		Items:     make([]string, len(res.Unmeasured)),
		Metrics:   make([]string, len(res.Unmeasured)),
	}
	for i, t := range res.Unmeasured {
		arg.CheckKeys[i], arg.Items[i], arg.Metrics[i] = t.CheckKey, t.Item, t.Metric
	}
	if err := q.MarkCheckStatesUnmeasured(ctx, arg); err != nil {
		return fmt.Errorf("mark unmeasured check states: %w", err)
	}
	return nil
}
