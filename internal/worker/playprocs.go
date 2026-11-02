// What the worker sees of the play uid from its own: the check behind the
// sweep between jobs (backlog 7.35).
package worker

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// livePIDsOfUID lists the processes under procRoot (/proc) whose real,
// effective or saved uid is uid, zombies left out: a zombie has already
// exited and only waits for its parent to collect it. It reads
// /proc/<pid>/status, which every uid may read, so the worker needs no
// privilege and no help from the uid it is checking. A process that exits
// between the listing and its read is simply gone (ENOENT, or ESRCH on
// Linux); an unreadable process table is an error, since "nothing found"
// would then mean nothing.
func livePIDsOfUID(procRoot string, uid uint32) ([]int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", procRoot, err)
	}
	want := strconv.FormatUint(uint64(uid), 10)
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "status"))
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read status of pid %d: %w", pid, err)
		}
		state, uids := statusFields(raw)
		if strings.HasPrefix(state, "Z") || strings.HasPrefix(state, "X") {
			continue
		}
		if len(uids) >= 3 && (uids[0] == want || uids[1] == want || uids[2] == want) {
			out = append(out, pid)
		}
	}
	return out, nil
}

// statusFields picks the State value and the Uid line's fields (real,
// effective, saved, filesystem) out of a /proc/<pid>/status document.
func statusFields(raw []byte) (state string, uids []string) {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		switch key {
		case "State":
			state = strings.TrimSpace(val)
		case "Uid":
			uids = strings.Fields(val)
		}
	}
	return state, uids
}
