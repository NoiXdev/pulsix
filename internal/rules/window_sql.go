// This file is the window fetch: every aggregate one host's rules ask for,
// in a single round trip. It is split from due_sql.go because it is the
// one statement in this package that is hand-written rather than
// sqlc-generated (a per-call list of requests is not a static query) and
// because the shape of that statement — one lateral aggregate per request,
// each served by check_samples_lookup_idx — is the whole reason windows
// cost one statement per host rather than one per rule.
package rules

import (
	"context"
	"fmt"
	"time"
)

// windowStatement aggregates check_samples once per request.
//
// The requests arrive as six parallel arrays rather than as a literal
// VALUES list so that this stays one prepared, parameterized statement
// whatever a host's rules ask for; unnest WITH ORDINALITY turns them back
// into rows and hands each one its index, which is how a result row finds
// its way back to the WindowRequest that asked for it without re-matching
// strings in Go.
//
// The lateral subquery is one index range scan per request: org_id,
// host_id, check_key, item and metric are check_samples_lookup_idx's
// leading columns in that order, and `at >= from` is the range condition
// its trailing `at DESC` column serves. There is deliberately no upper
// bound on `at` — the window is "the last N seconds", and the ingest
// path's own plausibility window already refuses anything more than two
// minutes ahead of the manager's clock.
//
// avg/min/max are the ordinary aggregates. rate is (last − first) /
// seconds, computed from TimescaleDB's own first()/last() aggregates —
// which take the value and the time column and so need neither a sort nor
// a second pass over the window. Over a window holding exactly one sample
// that is (v − v) / seconds = 0, which is the defined answer, not a
// division by zero: seconds is the rule's own window length and is never
// the number of samples. NULLIF guards the one way seconds could still be
// zero — a request built by hand rather than by a validated rule.
//
// `samples > 0` is what makes an empty window return no row at all: an
// aggregate over no rows yields one row of NULLs, and the outer filter
// drops it, so the caller's map simply has no entry for that request. That
// is what carries "a window containing no sample is a non-match" (the
// spec's own review focus 2) all the way to the evaluator, which treats a
// missing window exactly as it treats a missing metric. count(s.value)
// rather than count(*) for the same reason a discrete metric's samples —
// which carry a label and no value — contribute to nothing here.
const windowStatement = `
SELECT idx, value FROM (
  SELECT r.idx AS idx,
         CASE r.agg
           WHEN 'avg'  THEN a.avg_value
           WHEN 'min'  THEN a.min_value
           WHEN 'max'  THEN a.max_value
           WHEN 'rate' THEN (a.last_value - a.first_value) / NULLIF(r.seconds, 0)
         END AS value
  FROM unnest($3::text[], $4::text[], $5::text[], $6::text[], $7::timestamptz[], $8::double precision[])
       WITH ORDINALITY AS r(check_key, item, metric, agg, from_at, seconds, idx)
  CROSS JOIN LATERAL (
    SELECT count(s.value) AS samples,
           avg(s.value) AS avg_value,
           min(s.value) AS min_value,
           max(s.value) AS max_value,
           first(s.value, s.at) AS first_value,
           last(s.value, s.at) AS last_value
    FROM check_samples s
    WHERE s.org_id = $1 AND s.host_id = $2
      AND s.check_key = r.check_key AND s.item = r.item AND s.metric = r.metric
      AND s.at >= r.from_at AND s.value IS NOT NULL
  ) a
  WHERE a.samples > 0
) w
WHERE value IS NOT NULL`

// Windows implements EvalStore: every requested aggregate in one
// statement, measured back from now — the same now the evaluation itself
// is stamped with, so a window and the state it decides can never be
// judged against two different presents.
//
// A request whose window holds no numeric sample is absent from the
// result, never zero. The evaluator reads that absence as "this rule does
// not match", which is the whole reason the statement filters rather than
// coalescing.
func (s *EvalSQLStore) Windows(ctx context.Context, d Due, reqs []WindowRequest, now time.Time) (map[WindowRequest]float64, error) {
	if len(reqs) == 0 {
		return nil, nil
	}
	checkKeys := make([]string, len(reqs))
	items := make([]string, len(reqs))
	metrics := make([]string, len(reqs))
	aggs := make([]string, len(reqs))
	froms := make([]time.Time, len(reqs))
	seconds := make([]float64, len(reqs))
	for i, req := range reqs {
		checkKeys[i] = req.Key.CheckKey
		items[i] = req.Key.Item
		metrics[i] = req.Metric
		aggs[i] = req.Agg
		froms[i] = now.Add(-time.Duration(req.Seconds) * time.Second)
		seconds[i] = float64(req.Seconds)
	}

	rows, err := s.conn(ctx).Query(ctx, windowStatement,
		d.OrgID, d.HostID, checkKeys, items, metrics, aggs, froms, seconds)
	if err != nil {
		return nil, fmt.Errorf("fetch window aggregates: %w", err)
	}
	defer rows.Close()

	out := make(map[WindowRequest]float64, len(reqs))
	for rows.Next() {
		var idx int64
		var value float64
		if err := rows.Scan(&idx, &value); err != nil {
			return nil, fmt.Errorf("fetch window aggregates: %w", err)
		}
		if idx < 1 || int(idx) > len(reqs) {
			// Unreachable: WITH ORDINALITY numbers the rows it was given.
			// Guarded rather than trusted, since the alternative is a panic
			// inside a ticker.
			return nil, fmt.Errorf("window aggregate returned index %d for %d requests", idx, len(reqs))
		}
		out[reqs[idx-1]] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("fetch window aggregates: %w", err)
	}
	return out, nil
}
