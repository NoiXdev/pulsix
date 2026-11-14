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
// instead of piling another stuck check onto it.
func (p *scriptPlugin) checkFilesWithin(ctx context.Context, argv []string, timeout time.Duration) (path string, run []string, refusal string) {
	key := strings.Join(argv, "\x00")
	if p.checking.stuck(key) {
		return "", nil, textBusy
	}
	type outcome struct {
		path    string
		run     []string
		refusal string
	}
	st := &runState{}
	done := make(chan outcome, 1)
	go func() {
		o := outcome{refusal: textInternal}
		defer func() {
			// A panic's value may quote an argument: it is dropped, and
			// the command reported as textInternal.
			_ = recover()
			p.checking.exited(key, st)
			done <- o
		}()
		o.path, o.run, o.refusal = p.checkFiles(argv)
	}()
	select {
	case o := <-done:
		return o.path, o.run, o.refusal
	case <-ctx.Done():
		p.checking.giveUp(key, st)
		return "", nil, fmt.Sprintf("checking the files took longer than %ds", int64(timeout/time.Second))
	}
}

// checkFiles checks the program, then every argument that names a file
// by an absolute path (fileArgument), and returns the program's resolved
// path with argv to run: argv[0] as configured, each argument that named
// a regular file replaced by the checker's resolved path, so the process
// gets the files that were checked. An absolute argument that names no
// regular file is left as it is once the place it would live passes
// (CheckPlace): a script missing now must not be able to appear there
// before the run. When one does not pass it returns why the first one
// fails. It fails closed: a file or place the checker could not check
// stops the run like a refused one. A path is echoed only when it is the
// program or a file or place the checker inspected and refused (checker),
// and only the refusal's own reason goes with it.
func (p *scriptPlugin) checkFiles(argv []string) (path string, run []string, refusal string) {
	prog := argv[0]
	if !filepath.IsAbs(prog) {
		return "", nil, prog + ": not an absolute path"
	}
	path, err := p.check.Check(prog)
	if err != nil {
		if reason, ok := refusalReason(err); ok {
			return "", nil, prog + ": " + reason
		}
		if errors.Is(err, errNotFile) {
			return "", nil, prog + ": " + errNotFile.Error()
		}
		return "", nil, prog + ": " + textUncheckedProgram
	}
	run = append([]string(nil), argv...)
	for i, arg := range argv[1:] {
		file, ok, err := fileArgument(arg)
		if err != nil {
			return "", nil, textUncheckedArgument
		}
		if !ok {
			continue
		}
		resolved, err := p.check.Check(file)
		noFile := errors.Is(err, errNotFile)
		if noFile {
			err = p.check.CheckPlace(file)
		}
		if reason, ok := refusalReason(err); ok {
			return "", nil, arg + ": " + reason
		}
		switch {
		case err != nil:
			return "", nil, textUncheckedArgument
		case !noFile:
			run[i+1] = resolved
		}
	}
	return path, run, ""
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
