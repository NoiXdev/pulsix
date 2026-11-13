package phpfpm

import (
	"encoding/json"
	"strconv"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck"
)

// maxPage bounds a pool's page. Without the query key full the page is one
// small object (under 400 bytes for PHP 7.4 to 8.4); with full, which an
// operator may write into the entry, it lists every process at about
// 400 bytes each, so 1 MiB holds some 2 500 processes — more than
// pm.max_children of any one pool in practice — while 16 pools fetched at
// once stay within 16 MiB, not 16 × webcheck.MaxBody.
const maxPage = 1 << 20

// status is what the plugin reads from a pool's page.
type status struct {
	startTime           uint64
	accepted            uint64
	listenQueue         uint64
	idle, active, total uint64
	maxChildren, slow   uint64
}

// count is a non-negative integer of the page: a bare JSON number of
// digits only. A quoted number, a sign, a fraction, an exponent, null or a
// value above the uint64 range is not FPM's format.
type count struct {
	v   uint64
	set bool
}

func (c *count) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || b[0] < '0' || b[0] > '9' {
		return webcheck.ErrFormat
	}
	v, err := strconv.ParseUint(string(b), 10, 64)
	if err != nil {
		return webcheck.ErrFormat
	}
	c.v, c.set = v, true
	return nil
}

// fpmPage is FPM's ?json object, every key FPM 7.4 to 8.4 writes besides
// "memory peak" (8.4) and the processes of full, which are ignored like
// any other unknown key. The keys the plugin does not use are still
// decoded, so a page whose value of one has the wrong type is refused.
type fpmPage struct {
	Pool               string `json:"pool"`
	ProcessManager     string `json:"process manager"`
	StartTime          count  `json:"start time"`
	StartSince         count  `json:"start since"`
	AcceptedConn       count  `json:"accepted conn"`
	ListenQueue        count  `json:"listen queue"`
	MaxListenQueue     count  `json:"max listen queue"`
	ListenQueueLen     count  `json:"listen queue len"`
	IdleProcesses      count  `json:"idle processes"`
	ActiveProcesses    count  `json:"active processes"`
	TotalProcesses     count  `json:"total processes"`
	MaxActiveProcesses count  `json:"max active processes"`
	MaxChildrenReached count  `json:"max children reached"`
	SlowRequests       count  `json:"slow requests"`
}

// parseStatus reads a pool's page. It keeps no state, so it may run for
// several pools at once. The keys the metrics need must be present; any
// other shape is webcheck.ErrFormat.
func parseStatus(body []byte) (status, error) {
	var p fpmPage
	if err := json.Unmarshal(body, &p); err != nil {
		return status{}, webcheck.ErrFormat
	}
	for _, c := range []count{p.StartTime, p.AcceptedConn, p.ListenQueue, p.IdleProcesses, p.ActiveProcesses,
		p.TotalProcesses, p.MaxChildrenReached, p.SlowRequests} {
		if !c.set {
			return status{}, webcheck.ErrFormat
		}
	}
	return status{
		startTime:   p.StartTime.v,
		accepted:    p.AcceptedConn.v,
		listenQueue: p.ListenQueue.v,
		idle:        p.IdleProcesses.v,
		active:      p.ActiveProcesses.v,
		total:       p.TotalProcesses.v,
		maxChildren: p.MaxChildrenReached.v,
		slow:        p.SlowRequests.v,
	}, nil
}
