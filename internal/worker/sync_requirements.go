// The sync's requirements step (spec 2026-11-06-requirements-cache, goals
// 5 to 7): the sync installs the requirements of the commit it fetched
// through the same per-commit cache as the runs of that commit, so the
// repository page shows an install that would fail every run first, and
// the variable read gets the roles of that commit's entry.
//
// The step's lines go to the sync's log as stdout events {"text": …},
// ahead of its ok event; URL userinfo (a token used as a bare username
// included) is already removed from them by the requirements service, and
// galaxy never receives the sync claim's one secret, the git credential (the
// plan's Ruling 6). Unlike a run, the sync opens no entry to the play uid:
// only runs read it as that uid (Ruling 5).
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker/requirements"
)

// evStateStdout is the state of a line in the sync's log: the shape the
// run view renders as plain text.
const evStateStdout = "stdout"

// ensureRequirements installs the requirements of the clone's commit at dir
// into its per-commit cache, or reuses this worker's install of them, and
// returns the entry (the zero Entry when no file counts or the files list
// nothing). A step that fails ends the sync with
// "install galaxy requirements: <reason>", sent as the last line
// "Requirements: <that text>" first; a cancelled one sends no failure
// line. A line that cannot be sent stops the install and ends the sync
// with "send requirements log: <reason>".
func (e *SyncExecutor) ensureRequirements(ctx context.Context, dir, subpath string, env RunEnv, sink func(*workerpb.RunEvent) error) (requirements.Entry, error) {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	var sendErr error
	send := func(text string) {
		if sendErr != nil {
			return
		}
		if err := sink(&workerpb.RunEvent{State: evStateStdout, Payload: linePayload(text)}); err != nil {
			sendErr = fmt.Errorf("send requirements log: %w", err)
			stop()
		}
	}
	entry, err := e.installRequirements(ctx, dir, subpath, env, send)
	if sendErr != nil {
		return requirements.Entry{}, sendErr
	}
	if err == nil {
		return entry, nil
	}
	err = fmt.Errorf("install galaxy requirements: %w", err)
	if !errors.Is(err, context.Canceled) {
		send("Requirements: " + err.Error())
		if sendErr != nil {
			return requirements.Entry{}, sendErr
		}
	}
	return requirements.Entry{}, err
}

// installRequirements finds the counted requirements files of the clone
// through its confined root (a counted path that leads out of the clone
// fails the step), reads the commit only when one counts (the plan's
// Ruling 3) and ensures its entry.
func (e *SyncExecutor) installRequirements(ctx context.Context, dir, subpath string, env RunEnv, line func(string)) (requirements.Entry, error) {
	clone, err := OpenCloneFS(dir)
	if err != nil {
		return requirements.Entry{}, err
	}
	defer func() { _ = clone.Close() }()
	files, err := requirements.Find(clone, CleanRepoPath(subpath))
	if err != nil {
		return requirements.Entry{}, fmt.Errorf("find requirements: %w", err)
	}
	if files.Empty() {
		return requirements.Entry{}, nil
	}
	if e.Requirements == nil {
		return requirements.Entry{}, errors.New("no requirements installer wired")
	}
	commit, err := e.Fetcher.HeadCommit(ctx, dir)
	if err != nil {
		return requirements.Entry{}, err
	}
	return e.Requirements.Ensure(ctx, requirements.Request{
		CloneDir:     dir,
		Clone:        clone,
		Files:        files,
		Commit:       commit,
		RepoCacheDir: env.RepoCacheDir,
		Line:         line,
	})
}

// linePayload is a stdout event's payload for one line of text.
func linePayload(text string) string {
	b, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return `{"text":""}`
	}
	return string(b)
}
