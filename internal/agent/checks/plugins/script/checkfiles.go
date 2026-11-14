package script

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// checkFilesWithin runs checkFiles until ctx ends, which the round's
// timeout bounds: a stat on a hung network mount does not return, and the
// round must. A check given up on keeps the command's argv marked in
// p.checking until it returns, so later rounds report the command busy
// instead of piling another stuck check onto it. When ctx ends before
// its deadline (the agent stopping), err is ctx's error and the command
// is not reported.
func (p *scriptPlugin) checkFilesWithin(ctx context.Context, argv []string, timeout time.Duration) (refusal string, err error) {
	key := strings.Join(argv, "\x00")
	if p.checking.stuck(key) {
		return textBusy, nil
	}
	st := &runState{}
	done := make(chan string, 1)
	go func() {
		refusal := textInternal
		defer func() {
			// A panic's value may quote an argument: it is dropped, and
			// the command reported as textInternal.
			_ = recover()
			p.checking.exited(key, st)
			done <- refusal
		}()
		refusal = p.checkFiles(argv)
	}()
	select {
	case refusal := <-done:
		return refusal, nil
	case <-ctx.Done():
		p.checking.giveUp(key, st)
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", ctx.Err()
		}
		return fmt.Sprintf("checking the files took longer than %ds", int64(timeout/time.Second)), nil
	}
}

// checkFiles checks the program, then every argument that names a file
// by an absolute path (fileArgument), and returns why the first one that
// does not pass fails, or "" when all pass. An argument that names no
// regular file passes once the directories on its way did (errNotFile):
// a script missing now cannot appear there before the run. Nothing is
// rewritten: the command runs as configured. It fails closed: a path the
// checker could not check stops the run like a refused one. A path is
// echoed only when it is the program or one the checker refused
// (checker), and only the refusal's own reason goes with it.
func (p *scriptPlugin) checkFiles(argv []string) (refusal string) {
	prog := argv[0]
	if !filepath.IsAbs(prog) {
		return prog + ": not an absolute path"
	}
	if err := p.check.Check(prog); err != nil {
		if reason, ok := refusalReason(err); ok {
			return prog + ": " + reason
		}
		if errors.Is(err, errNotFile) {
			return prog + ": " + errNotFile.Error()
		}
		return prog + ": " + textUncheckedProgram
	}
	for _, arg := range argv[1:] {
		file, ok, err := fileArgument(arg)
		if err != nil {
			return textUncheckedArgument
		}
		if !ok {
			continue
		}
		err = p.check.Check(file)
		if reason, ok := refusalReason(err); ok {
			return arg + ": " + reason
		}
		if err != nil && !errors.Is(err, errNotFile) {
			return textUncheckedArgument
		}
	}
	return ""
}

// refusalReason is the reason of a refuse error in err's chain, without
// any text a wrapper added around it.
func refusalReason(err error) (string, bool) {
	var r refusal
	if !errors.As(err, &r) {
		return "", false
	}
	return string(r), true
}
