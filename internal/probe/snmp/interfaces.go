package snmp

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"

	"github.com/noixdev/pulsix/internal/probe"
)

// IF-MIB columns the interfaces option walks.
const (
	colIfDescr       = ".1.3.6.1.2.1.2.2.1.2"
	colIfAdminStatus = ".1.3.6.1.2.1.2.2.1.7"
	colIfOperStatus  = ".1.3.6.1.2.1.2.2.1.8"
	colIfInOctets    = ".1.3.6.1.2.1.2.2.1.10"
	colIfInErrors    = ".1.3.6.1.2.1.2.2.1.14"
	colIfOutOctets   = ".1.3.6.1.2.1.2.2.1.16"
	colIfOutErrors   = ".1.3.6.1.2.1.2.2.1.20"
	colIfName        = ".1.3.6.1.2.1.31.1.1.1.1"
	colIfHCInOctets  = ".1.3.6.1.2.1.31.1.1.1.6"
	colIfHCOutOctets = ".1.3.6.1.2.1.31.1.1.1.10"
)

// ifColumns are walked in this order; ifDescr, which every ifTable row
// has, decides which interfaces there are.
var ifColumns = []string{
	colIfDescr, colIfName, colIfAdminStatus, colIfOperStatus,
	colIfHCInOctets, colIfHCOutOctets, colIfInOctets, colIfOutOctets,
	colIfInErrors, colIfOutErrors,
}

// maxInterfaces bounds the interfaces one poll reports, the lowest
// ifIndexes first.
const maxInterfaces = 128

// ifAdminDown is ifAdminStatus down(2); ifOperUp is ifOperStatus up(1).
const (
	ifAdminDown = 2
	ifOperUp    = 1
)

// maxWalkRows bounds the rows one column walk reads, valid or not: a
// device that answers rows the probe cannot use (an index of several
// arcs) is not walked to its end.
const maxWalkRows = 8 * maxInterfaces

// errStopWalk ends a walk early from its callback.
var errStopWalk = errors.New("stop walk")

// ifRow is one interface as the device reported it.
type ifRow struct {
	index       int
	name, descr string // raw device strings
	admin, oper int64  // 0: not reported
	counters    ifCounters
	item        string // set by nameInterfaces
}

// rowIndex is the ifIndex of a column's row OID: exactly one arc after the
// column, within InterfaceIndex's 1..2147483647.
func rowIndex(column, oid string) (int, bool) {
	rest, ok := strings.CutPrefix(normalizeOID(oid), column+".")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(rest, 10, 31)
	if err != nil || n == 0 {
		return 0, false
	}
	return int(n), true
}

// readInterfaces walks the interface table: the first maxInterfaces
// interfaces by ifIndex from ifDescr (skipped reports that there were
// more), then every other column up to the last kept ifIndex. A failed
// ifDescr walk is an error; a failed walk of another column leaves that
// column unreported.
func readInterfaces(c client) (rows []ifRow, skipped bool, err error) {
	seen := map[int]bool{}
	err = boundedWalk(c, colIfDescr, func(pdu gosnmp.SnmpPDU) error {
		idx, ok := rowIndex(colIfDescr, pdu.Name)
		if !ok || seen[idx] {
			return nil
		}
		if len(rows) == maxInterfaces {
			skipped = true
			return errStopWalk
		}
		seen[idx] = true
		descr, _ := toText(pdu.Value)
		rows = append(rows, ifRow{index: idx, descr: descr})
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, skipped, nil
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].index < rows[j].index })
	pos := make(map[int]int, len(rows))
	for i, r := range rows {
		pos[r.index] = i
	}
	last := rows[len(rows)-1].index

	for _, col := range ifColumns[1:] {
		got := map[int]gosnmp.SnmpPDU{}
		err := boundedWalk(c, col, func(pdu gosnmp.SnmpPDU) error {
			idx, ok := rowIndex(col, pdu.Name)
			if !ok {
				return nil
			}
			if idx > last {
				return errStopWalk
			}
			if _, kept := pos[idx]; kept {
				got[idx] = pdu
			}
			return nil
		})
		if err != nil && !errors.Is(err, errStopWalk) {
			continue
		}
		for idx, pdu := range got {
			setColumn(&rows[pos[idx]], col, pdu)
		}
	}
	return rows, skipped, nil
}

// boundedWalk walks root, stopping after maxWalkRows rows.
func boundedWalk(c client, root string, fn gosnmp.WalkFunc) error {
	n := 0
	return c.walk(root, func(pdu gosnmp.SnmpPDU) error {
		if n++; n > maxWalkRows {
			return errStopWalk
		}
		return fn(pdu)
	})
}

// setColumn stores one column's value in r; a value of the wrong type is
// left unreported.
func setColumn(r *ifRow, col string, pdu gosnmp.SnmpPDU) {
	switch col {
	case colIfName:
		if pdu.Type == gosnmp.OctetString {
			r.name, _ = toText(pdu.Value)
		}
	case colIfAdminStatus, colIfOperStatus:
		v, ok := toInt(pdu.Value)
		if pdu.Type != gosnmp.Integer || !ok || v < 1 || v > 7 {
			return
		}
		if col == colIfAdminStatus {
			r.admin = v
		} else {
			r.oper = v
		}
	case colIfHCInOctets:
		r.counters.hcIn = readCounter(pdu)
	case colIfHCOutOctets:
		r.counters.hcOut = readCounter(pdu)
	case colIfInOctets:
		r.counters.in = readCounter(pdu)
	case colIfOutOctets:
		r.counters.out = readCounter(pdu)
	case colIfInErrors:
		r.counters.inErr = readCounter(pdu)
	case colIfOutErrors:
		r.counters.outErr = readCounter(pdu)
	}
}

// readCounter reads a Counter32 or Counter64; anything else is not a
// counter.
func readCounter(pdu gosnmp.SnmpPDU) counter {
	v, ok := toUint(pdu.Value)
	switch {
	case !ok:
		return counter{}
	case pdu.Type == gosnmp.Counter64:
		return counter{value: v, width: 64}
	case pdu.Type == gosnmp.Counter32 && v < 1<<32:
		return counter{value: v, width: 32}
	}
	return counter{}
}

// nameInterfaces sets each row's item: ifName, else ifDescr, cleaned; an
// interface without a name, or whose name the manager would refuse, is
// "if<ifIndex>". Names several interfaces share get " (<ifIndex>)"
// appended. A name that still collides — with another interface's
// suffixed name, or one that grew too long — falls back to "if<ifIndex>",
// then to "if<ifIndex> (<ifIndex>)"; an interface left without a unique
// name gets none and is not reported. rows are in ifIndex order, which
// decides who keeps a contested name.
func nameInterfaces(rows []ifRow) {
	count := map[string]int{}
	for i := range rows {
		r := &rows[i]
		r.item = cleanText(r.name)
		if r.item == "" {
			r.item = cleanText(r.descr)
		}
		if !reportableItem(r.item) {
			r.item = fallbackName(r.index)
		}
		count[r.item]++
	}
	taken := map[string]bool{}
	for i := range rows {
		r := &rows[i]
		candidates := []string{r.item}
		if count[r.item] > 1 {
			candidates[0] = fmt.Sprintf("%s (%d)", r.item, r.index)
		}
		candidates = append(candidates, fallbackName(r.index), fmt.Sprintf("if%d (%d)", r.index, r.index))
		r.item = ""
		for _, c := range candidates {
			if reportableItem(c) && !taken[c] {
				r.item = c
				break
			}
		}
		if r.item != "" {
			taken[r.item] = true
		}
	}
}

func fallbackName(index int) string { return "if" + strconv.Itoa(index) }

// interfaceStatus is snmp.if.status from ifAdminStatus and ifOperStatus:
// admin_down when the interface is administratively down, up when it is
// operationally up, down otherwise. ok is false when either is missing.
func interfaceStatus(admin, oper int64) (string, bool) {
	switch {
	case admin == 0 || oper == 0:
		return "", false
	case admin == ifAdminDown:
		return "admin_down", true
	case oper == ifOperUp:
		return "up", true
	}
	return "down", true
}

// interfaceObservations are the rows' status and, when prev is a usable
// earlier poll secs before (ratesOK), their rates against prev's counters.
func interfaceObservations(rows []ifRow, prev map[int]ifCounters, secs float64, ratesOK bool) []probe.Observation {
	var obs []probe.Observation
	for _, r := range rows {
		if r.item == "" {
			continue
		}
		if st, ok := interfaceStatus(r.admin, r.oper); ok {
			obs = append(obs, probe.Observation{Metric: "snmp.if.status", Item: r.item, Label: &st})
		} else {
			obs = append(obs, probe.Observation{Metric: "snmp.if.status", Item: r.item, Err: errNoStatus})
		}
		p, had := prev[r.index]
		if !ratesOK || !had {
			continue
		}
		rt := interfaceRates(p, r.counters, secs)
		if rt.hasIn {
			obs = append(obs, itemValue("snmp.if.in_bps", r.item, rt.inBps))
		}
		if rt.hasOut {
			obs = append(obs, itemValue("snmp.if.out_bps", r.item, rt.outBps))
		}
		if rt.hasErrs {
			obs = append(obs, itemValue("snmp.if.errors", r.item, rt.errs))
		}
	}
	return obs
}

func itemValue(metric, item string, v float64) probe.Observation {
	o := probe.Value(metric, v)
	o.Item = item
	return o
}
