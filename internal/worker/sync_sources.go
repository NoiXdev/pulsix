// The repo sync's read of the playbook variable sources (backlog 7.75):
// after the fetch and the requirements install, the executor reads every
// playbook's declared variables from the clone (internal/varsource) and
// ships the encoded report under the result's stats key var_sources; the
// ok event carries only a summary of it.
//
// Nothing in the read fails the sync: a commit that cannot be read, a
// subpath that cannot be opened, a parser panic or the time limit each
// leave a report that says what was not read, never a failed job. A report
// that read nothing of the repository is Failed, so the manager keeps the
// previous record. Only a cancellation of the job itself ends it, as
// cancelled.
//
// The read never leaves the clone's subpath and never enters git's own
// files (clonefs.go): a role's meta/argument_specs.yml linked to /etc/… or
// to .git/config is reported unreadable.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/noixdev/pulsix/internal/varsource"
)

// VarSourcesStatsKey is the sync result's stats key that carries the
// encoded variable sources report (a json.RawMessage). The manager takes it
// out before it stores the stats; a result without it is an older worker's.
const VarSourcesStatsKey = "var_sources"

// varSourcesTimeLimit bounds the whole read of a repository's sources, the
// commit, discovery and every playbook together. It is checked between
// files, before each candidate playbook: the playbook being read when it
// passes is read to its end (its own work is bounded by the parser), and
// the playbooks not read yet are left out.
const varSourcesTimeLimit = 60 * time.Second

// parseRepoFunc reads the sources of a repository subpath.
type parseRepoFunc func(ctx context.Context, fsys fs.FS, commit string) varsource.Report

// readVarSources reads the clone's variable sources under subpath and
// returns the encoded report and the ok event's summary of it. Without a
// report that encodes, both are nil (the manager then treats the sync as
// one that sent no sources and keeps the previous record).
func (e *SyncExecutor) readVarSources(ctx context.Context, dir, subpath string) (json.RawMessage, map[string]any) {
	limit := e.sourcesTimeLimit
	if limit <= 0 {
		limit = varSourcesTimeLimit
	}
	rctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	r := e.collectVarSources(rctx, dir, subpath)
	data, sent, err := varsource.Encode(r, varsource.MaxReportBytes)
	if err != nil {
		e.logger().WarnContext(ctx, "encode variable sources", "err", err)
		data, sent, err = varsource.Encode(unreadReport(r.Commit, "the variable sources are too large to send; no playbooks read"), varsource.MaxReportBytes)
		if err != nil {
			e.logger().ErrorContext(ctx, "encode variable sources", "err", err)
			return nil, nil
		}
	}
	return data, map[string]any{"commit": sent.Commit, "playbooks": len(sent.Playbooks), "warnings": warningCount(sent)}
}

// collectVarSources is the report of the clone's sources: the commit and
// the parse of the subpath. A panic of the parser leaves a report with no
// playbooks and one read_failed note.
func (e *SyncExecutor) collectVarSources(ctx context.Context, dir, subpath string) (r varsource.Report) {
	var commit string
	var notes []varsource.Note
	defer func() {
		if p := recover(); p != nil {
			e.logger().ErrorContext(ctx, "read variable sources: panic", "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			r = unreadReport(commit, "reading the variable sources failed; no playbooks read")
			r.Notes = append(notes, r.Notes...)
		}
	}()

	commit, err := e.Fetcher.HeadCommit(ctx, dir)
	if err != nil {
		e.logger().WarnContext(ctx, "read the clone's commit", "err", err)
		commit = ""
		notes = append(notes, varsource.ReadFailed("the commit of the repository could not be determined"))
	}
	fsys, err := openCloneFS(dir, subpath)
	if err != nil {
		e.logger().WarnContext(ctx, "open the repository subpath", "err", err)
		r = unreadReport(commit, "the repository subpath could not be opened; no playbooks discovered")
		r.Notes = append(notes, r.Notes...)
		return r
	}
	defer func() { _ = fsys.Close() }()

	parse := e.parseRepo
	if parse == nil {
		parse = varsource.ParseRepo
	}
	r = parse(ctx, fsys, commit)
	r.Notes = append(notes, r.Notes...)
	return r
}

// unreadReport is a Failed report: the repository was not read, one
// read_failed note says why.
func unreadReport(commit, why string) varsource.Report {
	return varsource.Report{Commit: commit, Failed: true, Playbooks: []varsource.Playbook{}, Notes: []varsource.Note{varsource.ReadFailed(why)}}
}

// warningCount counts the report's warnings, its own and its playbooks'.
func warningCount(r varsource.Report) int {
	n := 0
	count := func(notes []varsource.Note) {
		for _, note := range notes {
			if note.Level == varsource.LevelWarning {
				n++
			}
		}
	}
	count(r.Notes)
	for _, pb := range r.Playbooks {
		count(pb.Notes)
	}
	return n
}

func (e *SyncExecutor) logger() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}
