package supervisor

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// statReader returns the contents of /proc/<pid>/stat. It is a function so
// the walks below are tested over scripted process tables on any OS; the
// Linux system passes a reader of the real /proc.
type statReader func(pid int) ([]byte, error)

// maxChainHops bounds isDescendant's walk up the parent chain. A real chain
// is a handful of links and the kernel's cannot loop; the bound only keeps a
// table that changes under the walk from turning it into an endless one.
const maxChainHops = 4096

// descendantsFrom returns the live descendants of self among pids: it reads
// each pid's stat once, builds the parent → child map and walks it from
// self. Processes that have exited (see exited) are left out of the result,
// since they have nothing left to signal, only to reap, but the walk passes
// through them, so a child that has not been reparented yet is still found.
//
// An entry that cannot be read or parsed is skipped, never fatal. It may be
// a process that exited after /proc was listed, or one this uid may not read
// (hidepid), and either way it cannot be shown to be ours. Failing the whole
// listing over it would make stopTree signal nothing at all, and an
// unrelated process must never stop the supervisor from stopping its own
// tree.
func descendantsFrom(self int, pids []int, read statReader) []int {
	children := make(map[int][]int)
	dead := make(map[int]bool)
	for _, pid := range pids {
		fields, ppid, ok := readLink(pid, read)
		if !ok {
			continue
		}
		children[ppid] = append(children[ppid], pid)
		if exited(fields) {
			dead[pid] = true
		}
	}

	var out []int
	queue := []int{self}
	for len(queue) > 0 {
		next := children[queue[0]]
		queue = append(queue[1:], next...)
		for _, pid := range next {
			if !dead[pid] {
				out = append(out, pid)
			}
		}
	}
	return out
}

// isDescendant reports whether pid's parent chain reaches ancestor. It reads
// the stat of pid and of each parent above it, and nothing else, so one
// signal costs a few reads rather than a walk of all of /proc. A link that
// cannot be read, or a chain that ends at PID 1 or 0 without passing
// ancestor, means "not a descendant": the caller then leaves the process
// alone, which is the safe answer for a pid it cannot vouch for.
func isDescendant(ancestor, pid int, read statReader) bool {
	for range maxChainHops {
		_, ppid, ok := readLink(pid, read)
		if !ok {
			return false
		}
		if ppid == ancestor {
			return true
		}
		if ppid <= 1 {
			return false
		}
		pid = ppid
	}
	return false
}

// numThreadsField is num_threads' index in parseStat's fields: stat field
// 20, counted from the state's 3.
const numThreadsField = 17

// exited reports whether a process's parsed stat says it has exited: a dead
// task (X), or a zombie (Z) whose thread group has nothing left running. A
// thread-group leader whose main thread exited while other threads still
// run shows as Z too, but its num_threads counts those threads, and a
// signal to it still reaches them: that group is alive. A stat too short to
// carry num_threads is taken at its state.
func exited(fields []string) bool {
	switch fields[0] {
	case "X":
		return true
	case "Z":
		if len(fields) > numThreadsField {
			if n, err := strconv.Atoi(fields[numThreadsField]); err == nil && n > 1 {
				return false
			}
		}
		return true
	}
	return false
}

// readLink reads pid's stat fields and parent, reporting false for a stat
// that cannot be read or parsed.
func readLink(pid int, read statReader) (fields []string, ppid int, ok bool) {
	b, err := read(pid)
	if err != nil {
		return nil, 0, false
	}
	fields, err = parseStat(b)
	if err != nil {
		return nil, 0, false
	}
	ppid, err = strconv.Atoi(fields[1])
	if err != nil {
		return nil, 0, false
	}
	return fields, ppid, true
}

// parseStat returns the fields of a /proc/<pid>/stat line that follow the
// command name: [0] state, [1] ppid, [2] pgrp, [3] session, and so on. The
// command name is in parentheses and may itself contain spaces, parentheses
// or newlines, so the fields start after the last ')', not after the
// second space.
func parseStat(stat []byte) ([]string, error) {
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return nil, errors.New("no command name in stat")
	}
	fields := strings.Fields(string(stat[i+1:]))
	if len(fields) < 4 {
		return nil, fmt.Errorf("stat has %d fields after the command name, want at least 4", len(fields))
	}
	return fields, nil
}
