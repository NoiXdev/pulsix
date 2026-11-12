package smart

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// device is one smartctl target: the path handed to smartctl, the -d type
// the scan named (empty for a configured device) and the item it reports
// under.
type device struct {
	item string
	path string
	typ  string
}

var scanArgs = []string{"--scan", "--json"}

// args is the fixed argument list for one device. The path is always one
// argv element and always last. -n standby,0 leaves a sleeping disk asleep
// and makes smartctl exit 0 for it, with JSON that carries no data blocks:
// the disk reports nothing that round.
func (d device) args() []string {
	a := []string{"--json", "-a", "-n", "standby,0"}
	if d.typ != "" {
		a = append(a, "-d", d.typ)
	}
	return append(a, d.path)
}

// scanReport is the part of `smartctl --scan --json` the plugin reads:
// devices[].name (the path to query) and devices[].type (the -d value).
type scanReport struct {
	Smartctl smartctlBlock `json:"smartctl"`
	Devices  []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"devices"`
}

// scanType is a plain smartctl device type: "sat", "nvme", "sat,12",
// "megaraid,0", "usbjmicron,p,1", "areca,3/1", "hpt,1/1/2". Anything else
// is not passed on.
var scanType = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9,._+/-]{0,63}$`)

// controllerFamilies are the -d type families (the text before the first
// comma) that address a disk behind a RAID controller, where several disks
// share one device name and differ only by the type's arguments.
var controllerFamilies = map[string]bool{
	"megaraid": true, "sat+megaraid": true, "cciss": true, "3ware": true,
	"areca": true, "aacraid": true, "sssraid": true, "hpt": true,
}

// scanItem names a scanned device: "<name> [<type>]" for a disk behind a
// RAID controller (controllerFamilies), the name otherwise. It depends on
// the entry alone, so a disk keeps its item when a sibling comes or goes.
func scanItem(name, typ string) string {
	family, _, _ := strings.Cut(strings.ToLower(typ), ",")
	if controllerFamilies[family] {
		return name + " [" + typ + "]"
	}
	return name
}

// parseScan turns a scan into the devices to query. A scanned name the
// manager would refuse as an item, or that smartctl would read as an
// option, is left out and named (quoted) in the returned errors, as is an
// entry with an odd type. Repeated entries are queried once; a different
// entry whose item another one already took is left out and named.
func parseScan(res runResult) ([]device, []error, error) {
	var rep scanReport
	if !isJSONObject(res.stdout) || json.Unmarshal(res.stdout, &rep) != nil {
		return nil, nil, fmt.Errorf("smart: smartctl --scan %s", noJSON(res.exitCode))
	}
	if res.exitCode&exitCommandLine != 0 || (res.exitCode != 0 && len(rep.Devices) == 0) {
		return nil, nil, fmt.Errorf("smart: smartctl --scan failed (exit status %d)%s", res.exitCode, rep.Smartctl.detail())
	}

	var errs []error
	var devs []device
	taken := map[string]device{}
	for _, e := range rep.Devices {
		if err := queryable(e.Name); err != nil {
			errs = append(errs, fmt.Errorf("smart: device %q from the scan cannot be queried: %w", e.Name, err))
			continue
		}
		if e.Type != "" && !scanType.MatchString(e.Type) {
			errs = append(errs, fmt.Errorf("smart: device %q from the scan has a device type smartctl is not handed: %q", e.Name, e.Type))
			continue
		}
		d := device{item: scanItem(e.Name, e.Type), path: e.Name, typ: e.Type}
		if !checks.ReportableItem(d.item) {
			errs = append(errs, fmt.Errorf("smart: device %q cannot be reported: longer than %d bytes", d.item, checks.MaxItemLen))
			continue
		}
		if prev, ok := taken[d.item]; ok {
			if prev != d {
				errs = append(errs, fmt.Errorf("smart: device %q -d %q from the scan is left out: its item is taken by -d %q", e.Name, e.Type, prev.typ))
			}
			continue
		}
		taken[d.item] = d
		devs = append(devs, d)
	}
	return devs, errs, nil
}

// configuredDevices turns the devices parameter into the devices to query:
// exactly those, each once, in the order given, without a type. The
// manager has validated them; an entry that still could not be queried
// safely is left out and named.
func configuredDevices(paths []string) ([]device, []error) {
	var errs []error
	seen := make(map[string]bool, len(paths))
	devs := make([]device, 0, len(paths))
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		if err := queryable(p); err != nil {
			errs = append(errs, fmt.Errorf("smart: device %q cannot be queried: %w", p, err))
			continue
		}
		devs = append(devs, device{item: p, path: p})
	}
	return devs, errs
}

// queryable reports why name cannot be a smartctl argument and an item:
// it must be non-empty, must not start with "-" (smartctl would read an
// option), and must pass the manager's item contract (which also rules out
// NUL and every other control character).
func queryable(name string) error {
	switch {
	case name == "":
		return errors.New("empty name")
	case strings.HasPrefix(name, "-"):
		return errors.New("starts with \"-\"")
	case !checks.ReportableItem(name):
		return fmt.Errorf("longer than %d bytes, not valid UTF-8 or carrying a control character", checks.MaxItemLen)
	}
	return nil
}
