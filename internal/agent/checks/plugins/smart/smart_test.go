package smart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// TestSmartNewFindsSmartctl: the wired plugin looks smartctl up and either
// finds it or reports the install hint (smartctl is not run).
func TestSmartNewFindsSmartctl(t *testing.T) {
	r, err := New().(smartPlugin).smartctl()
	switch {
	case errors.Is(err, errNotFound):
	case err != nil:
		t.Fatalf("smartctl() error = %v", err)
	default:
		if er, ok := r.(execRunner); !ok || er.path == "" || er.busy == nil {
			t.Fatalf("smartctl() = %#v", r)
		}
	}
}

func TestSmartName(t *testing.T) {
	if got := New().Name(); got != "smart" {
		t.Fatalf("Name() = %q, want smart", got)
	}
}

// TestSmartScansAndReportsEveryDevice: without a devices parameter the
// plugin scans, then queries every device with the type the scan named,
// and reports each metric the device states — ATA attributes for the SATA
// disk, the NVMe health log for the NVMe disk, nothing for the USB stick
// that has no SMART at all.
func TestSmartScansAndReportsEveryDevice(t *testing.T) {
	f := newFakeRunner(t, map[string]reply{
		"scan":               {stdout: fixture(t, "scan.json")},
		"/dev/sda -d sat":    {stdout: fixture(t, "ata.json")},
		"/dev/sdb -d scsi":   {stdout: fixture(t, "usb-no-smart.json"), exit: 4},
		"/dev/nvme0 -d nvme": {stdout: fixture(t, "nvme.json")},
	})
	obs, err := plugin(f).Collect(context.Background(), nil)
	if err != nil {
		t.Fatalf("Collect error = %v", err)
	}

	calls := f.callArgs()
	if !slices.Equal(calls[0], []string{"--scan", "--json"}) {
		t.Errorf("scan argv = %q", calls[0])
	}
	wantArgv := []string{"--json", "-a", "-n", "standby", "-d", "sat", "/dev/sda"}
	if !slices.ContainsFunc(calls, func(c []string) bool { return slices.Equal(c, wantArgv) }) {
		t.Errorf("calls = %q, want one with argv %q", calls, wantArgv)
	}

	ata := map[string]float64{
		"smart.passed":                1,
		"smart.temperature_celsius":   34,
		"smart.power_on_hours":        21843,
		"smart.reallocated_sectors":   3,
		"smart.pending_sectors":       1,
		"smart.offline_uncorrectable": 0,
	}
	nvme := map[string]float64{
		"smart.passed":                   1,
		"smart.temperature_celsius":      41,
		"smart.power_on_hours":           6122,
		"smart.nvme_percentage_used":     2,
		"smart.nvme_available_spare_pct": 100,
		"smart.nvme_media_errors":        0,
	}
	for item, want := range map[string]map[string]float64{"/dev/sda": ata, "/dev/nvme0": nvme} {
		if got := len(itemObs(obs, item)); got != len(want) {
			t.Errorf("%s: %d observations, want %d: %+v", item, got, len(want), itemObs(obs, item))
		}
		for metric, v := range want {
			if got := value(t, obs, item, metric); got != v {
				t.Errorf("%s %s = %v, want %v", item, metric, got, v)
			}
		}
	}
	if got := itemObs(obs, "/dev/sdb"); len(got) != 0 {
		t.Errorf("/dev/sdb (no SMART) reported %+v, want nothing", got)
	}
	for _, o := range obs {
		encodesAsCheckResult(t, o)
	}
}

// TestSmartFailingDiskExitBitsAreData: smartctl sets bits 3–7 of its exit
// status when the disk reports SMART findings. Those are the readings, not
// a collection failure.
func TestSmartFailingDiskExitBitsAreData(t *testing.T) {
	for _, exit := range []int{8, 16, 32, 64, 128, 88, 8 | 4} {
		t.Run(fmt.Sprint(exit), func(t *testing.T) {
			f := newFakeRunner(t, map[string]reply{"/dev/sdc": {stdout: fixture(t, "failing.json"), exit: exit}})
			obs, err := plugin(f).Collect(context.Background(), map[string]any{"devices": []string{"/dev/sdc"}})
			if err != nil {
				t.Fatalf("Collect error = %v", err)
			}
			for metric, want := range map[string]float64{
				"smart.passed":                0,
				"smart.temperature_celsius":   38,
				"smart.power_on_hours":        50512,
				"smart.reallocated_sectors":   1832,
				"smart.pending_sectors":       46,
				"smart.offline_uncorrectable": 17,
			} {
				if got := value(t, obs, "/dev/sdc", metric); got != want {
					t.Errorf("%s = %v, want %v", metric, got, want)
				}
			}
			if len(obs) != 6 {
				t.Errorf("got %d observations, want 6: %+v", len(obs), obs)
			}
		})
	}
}

// TestSmartStandbyReportsNothing: -n standby makes smartctl leave a
// sleeping disk alone and exit with bit 1. That disk reports nothing this
// round — no reading, no error — and the others still report.
func TestSmartStandbyReportsNothing(t *testing.T) {
	for _, tc := range []struct {
		desc   string
		stdout []byte
	}{
		{"json message", fixture(t, "standby.json")},
		{"plain text", []byte("smartctl 7.3 2022-02-28 r5338\nDevice is in STANDBY mode, exit(2)\n")},
		{"sleep mode", []byte(`{"smartctl":{"messages":[{"string":"Device is in SLEEP mode, exit(2)","severity":"information"}],"exit_status":2}}`)},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			f := newFakeRunner(t, map[string]reply{
				"/dev/sdd": {stdout: tc.stdout, exit: 2},
				"/dev/sda": {stdout: fixture(t, "ata.json")},
			})
			obs, err := plugin(f).Collect(context.Background(), map[string]any{"devices": []any{"/dev/sdd", "/dev/sda"}})
			if err != nil {
				t.Fatalf("Collect error = %v", err)
			}
			if got := itemObs(obs, "/dev/sdd"); len(got) != 0 {
				t.Errorf("standby disk reported %+v, want nothing", got)
			}
			if got := value(t, obs, "/dev/sda", "smart.passed"); got != 1 {
				t.Errorf("/dev/sda passed = %v", got)
			}
		})
	}
}

// TestSmartNoSMARTBlocksReportsNothing is Review Focus 1: valid JSON
// without the blocks the metrics come from (a USB stick, a virtual disk)
// is not a failure. The device reports only what it states, here nothing.
func TestSmartNoSMARTBlocksReportsNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		exit int
	}{
		{"usb-no-smart.json", 4},
		{"virtual-disk.json", 0},
		{"standby.json", 0}, // a device block and nothing else
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRunner(t, map[string]reply{"/dev/sdx": {stdout: fixture(t, tc.name), exit: tc.exit}})
			obs, err := plugin(f).Collect(context.Background(), map[string]any{"devices": []string{"/dev/sdx"}})
			if err != nil || len(obs) != 0 {
				t.Fatalf("Collect = %+v, %v; want nothing and no error", obs, err)
			}
		})
	}
}

// TestSmartPartialReport: a block that is there but incomplete reports the
// fields it has, and only those.
func TestSmartPartialReport(t *testing.T) {
	stdout := []byte(`{
		"smart_status": {"passed": null},
		"power_on_time": {"hours": null},
		"temperature": {"current": 29},
		"ata_smart_attributes": {"table": [{"id": 5, "raw": {}}, {"id": 197, "raw": {"value": 4}}]},
		"nvme_smart_health_information_log": {"media_errors": 7}
	}`)
	f := newFakeRunner(t, map[string]reply{"/dev/sdx": {stdout: stdout}})
	obs, err := plugin(f).Collect(context.Background(), map[string]any{"devices": []string{"/dev/sdx"}})
	if err != nil {
		t.Fatalf("Collect error = %v", err)
	}
	if len(obs) != 3 {
		t.Fatalf("got %+v, want temperature, pending sectors and media errors only", obs)
	}
	for metric, want := range map[string]float64{
		"smart.temperature_celsius": 29,
		"smart.pending_sectors":     4,
		"smart.nvme_media_errors":   7,
	} {
		if got := value(t, obs, "/dev/sdx", metric); got != want {
			t.Errorf("%s = %v, want %v", metric, got, want)
		}
	}
}

// TestSmartNVMeFallbacks: an older smartctl that states temperature and
// power-on hours only inside the NVMe health log still reports them.
func TestSmartNVMeFallbacks(t *testing.T) {
	stdout := []byte(`{"smart_status":{"passed":true},"nvme_smart_health_information_log":{"temperature":45,"power_on_hours":100,"percentage_used":120,"available_spare":3,"media_errors":2}}`)
	f := newFakeRunner(t, map[string]reply{"/dev/nvme1": {stdout: stdout}})
	obs, err := plugin(f).Collect(context.Background(), map[string]any{"devices": []string{"/dev/nvme1"}})
	if err != nil {
		t.Fatalf("Collect error = %v", err)
	}
	for metric, want := range map[string]float64{
		"smart.passed":                   1,
		"smart.temperature_celsius":      45,
		"smart.power_on_hours":           100,
		"smart.nvme_percentage_used":     120,
		"smart.nvme_available_spare_pct": 3,
		"smart.nvme_media_errors":        2,
	} {
		if got := value(t, obs, "/dev/nvme1", metric); got != want {
			t.Errorf("%s = %v, want %v", metric, got, want)
		}
	}
}

// TestSmartWrongFieldTypeIsAdvisory: a field of an unexpected type is left
// out and named in the advisory error; the rest of the report stands.
func TestSmartWrongFieldTypeIsAdvisory(t *testing.T) {
	stdout := []byte(`{"smart_status":{"passed":"yes"},"temperature":{"current":31}}`)
	f := newFakeRunner(t, map[string]reply{"/dev/sdx": {stdout: stdout}})
	obs, err := plugin(f).Collect(context.Background(), map[string]any{"devices": []string{"/dev/sdx"}})
	if err == nil {
		t.Fatal("Collect error = nil, want the type mismatch named")
	}
	if len(obs) != 1 || value(t, obs, "/dev/sdx", "smart.temperature_celsius") != 31 {
		t.Fatalf("observations = %+v, want only the temperature", obs)
	}
}

// TestSmartDeviceFailures: the failures that are real — command line
// refused, device not opened, no JSON, the runner itself failing — give
// that device exactly one error observation on smart.passed and cost the
// other devices nothing.
func TestSmartDeviceFailures(t *testing.T) {
	for _, tc := range []struct {
		desc string
		r    reply
		want string
	}{
		{"open failed", reply{stdout: fixture(t, "open-failed.json"), exit: 2}, "No such device"},
		{"unknown USB bridge", reply{stdout: fixture(t, "usb-unknown-bridge.json"), exit: 1}, "Unknown USB bridge"},
		{"smartctl without JSON", reply{stdout: fixture(t, "old-smartctl.txt"), exit: 1}, "7.0 or later"},
		{"garbage", reply{stdout: []byte("{\"smartctl\": tru"), exit: 0}, "did not return JSON"},
		{"empty output", reply{exit: 0}, "did not return JSON"},
		{"JSON that is not an object", reply{stdout: []byte(`[1,2]`), exit: 0}, "did not return JSON"},
		{"bit 2 without JSON", reply{stdout: []byte("SMART command failed"), exit: 4}, "did not return JSON"},
		{"open failed with bits 3-7", reply{stdout: fixture(t, "open-failed.json"), exit: 2 | 64}, "could not open"},
		{"runner error", reply{err: errors.New("exec: permission denied")}, "permission denied"},
		{"output too large", reply{err: errOutputTooLarge}, "1 MiB"},
		{"still running", reply{err: errStillRunning}, "has not exited"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			f := newFakeRunner(t, map[string]reply{
				"/dev/sde": tc.r,
				"/dev/sda": {stdout: fixture(t, "ata.json")},
			})
			obs, err := plugin(f).Collect(context.Background(), map[string]any{"devices": []string{"/dev/sde", "/dev/sda"}})
			if err == nil || !strings.Contains(err.Error(), "/dev/sde") {
				t.Fatalf("Collect error = %v, want one naming /dev/sde", err)
			}
			onlyErr(t, obs, "/dev/sde", tc.want)
			if got := value(t, obs, "/dev/sda", "smart.passed"); got != 1 {
				t.Errorf("/dev/sda passed = %v", got)
			}
			for _, o := range obs {
				encodesAsCheckResult(t, o)
			}
		})
	}
}

// TestSmartErrorTextIsShortAndClean: smartctl's own messages are quoted
// cut and on one line.
func TestSmartErrorTextIsShortAndClean(t *testing.T) {
	msg := "open failed:\n" + strings.Repeat("x", 5000)
	stdout, _ := json.Marshal(map[string]any{"smartctl": map[string]any{"messages": []any{map[string]string{"string": msg, "severity": "error"}}}})
	f := newFakeRunner(t, map[string]reply{"/dev/sde": {stdout: stdout, exit: 2}})
	obs, _ := plugin(f).Collect(context.Background(), map[string]any{"devices": []string{"/dev/sde"}})
	o := findObs(t, obs, "/dev/sde", "smart.passed")
	if len(o.Err) > 512 || strings.ContainsAny(o.Err, "\n\r") {
		t.Fatalf("error text (%d bytes) = %q", len(o.Err), o.Err)
	}
}

func TestSmartMissingSmartctl(t *testing.T) {
	p := smartPlugin{smartctl: func() (runner, error) { return nil, errNotFound }}
	obs, err := p.Collect(context.Background(), nil)
	if err == nil {
		t.Fatal("Collect error = nil")
	}
	if len(obs) != 1 || obs[0].Item != "" || obs[0].Metric != "smart.passed" || obs[0].Err != "smartctl not found; install smartmontools" {
		t.Fatalf("observations = %+v, want one install hint on item \"\"", obs)
	}
}

func TestSmartScanFailures(t *testing.T) {
	for _, tc := range []struct {
		desc string
		r    reply
		want string
	}{
		{"no devices", reply{stdout: fixture(t, "scan-empty.json")}, "found no devices"},
		{"smartctl without JSON", reply{stdout: fixture(t, "old-smartctl.txt"), exit: 1}, "7.0 or later"},
		{"refused", reply{stdout: []byte(`{"smartctl":{"messages":[{"string":"bad option","severity":"error"}],"exit_status":1}}`), exit: 1}, "bad option"},
		{"runner error", reply{err: errors.New("exec format error")}, "exec format error"},
		{"hangs", reply{hang: true}, "did not answer within"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			f := newFakeRunner(t, map[string]reply{"scan": tc.r})
			p := plugin(f)
			p.deviceTimeout = 50 * time.Millisecond
			obs, err := p.Collect(context.Background(), map[string]any{"devices": []string{}})
			if err == nil {
				t.Fatal("Collect error = nil")
			}
			if len(obs) != 1 || obs[0].Item != "" || obs[0].Metric != "smart.passed" || !strings.Contains(obs[0].Err, tc.want) {
				t.Fatalf("observations = %+v, want one error on item \"\" containing %q", obs, tc.want)
			}
		})
	}
}

// TestSmartDevicesParamSkipsScan: a devices list is queried exactly as
// given (deduplicated, no scan, no -d type), each item named as given.
func TestSmartDevicesParamSkipsScan(t *testing.T) {
	f := newFakeRunner(t, map[string]reply{
		"/dev/sda":   {stdout: fixture(t, "ata.json")},
		"/dev/nvme0": {stdout: fixture(t, "nvme.json")},
	})
	obs, err := plugin(f).Collect(context.Background(), map[string]any{"devices": []any{"/dev/sda", "/dev/nvme0", "/dev/sda"}})
	if err != nil {
		t.Fatalf("Collect error = %v", err)
	}
	calls := f.callArgs()
	if len(calls) != 2 {
		t.Fatalf("calls = %q, want one per distinct device and no scan", calls)
	}
	for _, c := range calls {
		if slices.Contains(c, "--scan") || slices.Contains(c, "-d") {
			t.Errorf("argv %q: want no scan and no -d", c)
		}
	}
	if got := items(obs); !slices.Equal(got, []string{"/dev/nvme0", "/dev/sda"}) {
		t.Errorf("items = %q", got)
	}
}

func TestSmartBadParams(t *testing.T) {
	f := newFakeRunner(t, nil)
	if _, err := plugin(f).Collect(context.Background(), map[string]any{"devices": "/dev/sda"}); err == nil {
		t.Fatal("Collect error = nil for a devices string")
	}
}

// TestSmartOddDeviceNames is Review Focus 3: whatever the scan names a
// device, it is handed to smartctl as one argument and is the item; a name
// the manager would refuse as an item, or one smartctl would read as an
// option, is not queried and is named in the advisory error.
func TestSmartOddDeviceNames(t *testing.T) {
	good := []string{
		"/dev/disk/by-id/ata-Samsung SSD 860 EVO 500GB_S3Z1NB0K",
		`\\.\PhysicalDrive0`,
		"/dev/disk/by-id/nvme-eui.0025385b71b0a7c0",
		"/dev/csmi0,1",
	}
	bad := []string{"", "-x", "--json", "/dev/sd\x00a", "/dev/a\nb", "/dev/" + strings.Repeat("s", 600)}
	var devs []map[string]string
	replies := map[string]reply{}
	for _, n := range append(slices.Clone(good), bad...) {
		devs = append(devs, map[string]string{"name": n, "type": "sat"})
	}
	for _, n := range good {
		replies[n+" -d sat"] = reply{stdout: fixture(t, "ata.json")}
	}
	scan, _ := json.Marshal(map[string]any{"devices": devs})
	replies["scan"] = reply{stdout: scan}
	f := newFakeRunner(t, replies)

	obs, err := plugin(f).Collect(context.Background(), nil)
	if err == nil {
		t.Fatal("Collect error = nil, want the skipped devices named")
	}
	for _, b := range bad[1:] {
		if !strings.Contains(err.Error(), fmt.Sprintf("%q", b)) {
			t.Errorf("error %q does not name %q", err, b)
		}
	}
	for _, c := range f.callArgs()[1:] {
		if dev := c[len(c)-1]; !slices.Contains(good, dev) {
			t.Errorf("queried %q", dev)
		}
	}
	want := slices.Clone(good)
	slices.Sort(want)
	if got := items(obs); !slices.Equal(got, want) {
		t.Errorf("items = %q, want %q", got, want)
	}
	for _, o := range obs {
		encodesAsCheckResult(t, o)
	}
}

// TestSmartBadScanType: a device type that is not a plain smartctl type
// name is not passed on.
func TestSmartBadScanType(t *testing.T) {
	scan := []byte(`{"devices":[{"name":"/dev/sda","type":"-x"},{"name":"/dev/sdb","type":"sat\u0000"},{"name":"/dev/sdc","type":"sat,12"}]}`)
	f := newFakeRunner(t, map[string]reply{
		"scan":               {stdout: scan},
		"/dev/sdc -d sat,12": {stdout: fixture(t, "ata.json")},
	})
	obs, err := plugin(f).Collect(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "/dev/sda") || !strings.Contains(err.Error(), "/dev/sdb") {
		t.Fatalf("Collect error = %v, want /dev/sda and /dev/sdb named", err)
	}
	if got := items(obs); !slices.Equal(got, []string{"/dev/sdc"}) {
		t.Fatalf("items = %q", got)
	}
}

// TestSmartRepeatedScanNamesStayDistinct: disks behind one RAID controller
// share a device name and differ by type; each keeps its own item.
func TestSmartRepeatedScanNamesStayDistinct(t *testing.T) {
	f := newFakeRunner(t, map[string]reply{
		"scan":                     {stdout: fixture(t, "scan-megaraid.json")},
		"/dev/sda -d scsi":         {stdout: fixture(t, "virtual-disk.json")},
		"/dev/bus/0 -d megaraid,0": {stdout: fixture(t, "ata.json")},
		"/dev/bus/0 -d megaraid,1": {stdout: fixture(t, "failing.json"), exit: 8},
	})
	obs, err := plugin(f).Collect(context.Background(), nil)
	if err != nil {
		t.Fatalf("Collect error = %v", err)
	}
	if n := len(f.callArgs()); n != 4 {
		t.Errorf("%d smartctl calls, want scan + 3 (the repeated entry once)", n)
	}
	if got := value(t, obs, "/dev/bus/0 [megaraid,0]", "smart.passed"); got != 1 {
		t.Errorf("megaraid,0 passed = %v", got)
	}
	if got := value(t, obs, "/dev/bus/0 [megaraid,1]", "smart.passed"); got != 0 {
		t.Errorf("megaraid,1 passed = %v", got)
	}
}

// TestSmartHungDeviceDoesNotHoldOthers is Review Focus 5: one device that
// never answers gets a timeout error after the per-device bound; the
// others report.
func TestSmartHungDeviceDoesNotHoldOthers(t *testing.T) {
	f := newFakeRunner(t, map[string]reply{
		"/dev/sda":   {stdout: fixture(t, "ata.json")},
		"/dev/sdh":   {hang: true},
		"/dev/nvme0": {stdout: fixture(t, "nvme.json")},
	})
	p := plugin(f)
	p.deviceTimeout = 100 * time.Millisecond
	start := time.Now()
	obs, err := p.Collect(context.Background(), map[string]any{"devices": []string{"/dev/sda", "/dev/sdh", "/dev/nvme0"}})
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Collect took %v", d)
	}
	if err == nil {
		t.Fatal("Collect error = nil")
	}
	onlyErr(t, obs, "/dev/sdh", "did not answer within 100ms")
	if value(t, obs, "/dev/sda", "smart.passed") != 1 || value(t, obs, "/dev/nvme0", "smart.passed") != 1 {
		t.Fatal("the answering devices did not report")
	}
}

// TestSmartAtMostFourAtOnce: no more than four smartctl processes run at
// once, however many devices there are.
func TestSmartAtMostFourAtOnce(t *testing.T) {
	replies := map[string]reply{}
	var devs []string
	for i := range 12 {
		d := fmt.Sprintf("/dev/sd%c", 'a'+i)
		devs = append(devs, d)
		replies[d] = reply{stdout: fixture(t, "ata.json"), delay: 20 * time.Millisecond}
	}
	f := newFakeRunner(t, replies)
	obs, err := plugin(f).Collect(context.Background(), map[string]any{"devices": devs})
	if err != nil {
		t.Fatalf("Collect error = %v", err)
	}
	if len(items(obs)) != 12 {
		t.Fatalf("items = %q", items(obs))
	}
	if f.maxInFlight > maxParallel || f.maxInFlight < 2 {
		t.Fatalf("max concurrent smartctl runs = %d, want 2..%d", f.maxInFlight, maxParallel)
	}
}

// TestSmartCollectionDeadline: the whole collection ends at its own bound.
// Devices still running are stopped, devices not yet started are not
// started, and every one of them gets an error.
func TestSmartCollectionDeadline(t *testing.T) {
	replies := map[string]reply{}
	var devs []string
	for i := range 8 {
		d := fmt.Sprintf("/dev/sd%c", 'a'+i)
		devs = append(devs, d)
		replies[d] = reply{hang: true}
	}
	f := newFakeRunner(t, replies)
	p := plugin(f)
	p.deviceTimeout = 100 * time.Millisecond
	p.collectTimeout = 150 * time.Millisecond
	start := time.Now()
	obs, err := p.Collect(context.Background(), map[string]any{"devices": devs})
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Collect took %v", d)
	}
	if err == nil {
		t.Fatal("Collect error = nil")
	}
	for _, d := range devs {
		if got := itemObs(obs, d); len(got) != 1 || got[0].Err == "" {
			t.Errorf("%s: %+v, want one error", d, got)
		}
	}
}

// TestSmartRunnerIgnoringTheDeadline: even a runner that does not return
// when told to cannot hold the collection past its bound.
func TestSmartRunnerIgnoringTheDeadline(t *testing.T) {
	f := newFakeRunner(t, map[string]reply{
		"/dev/sda": {stdout: fixture(t, "ata.json")},
		"/dev/sdh": {ignoreCtx: true},
	})
	p := plugin(f)
	p.collectTimeout = 150 * time.Millisecond
	start := time.Now()
	obs, err := p.Collect(context.Background(), map[string]any{"devices": []string{"/dev/sda", "/dev/sdh"}})
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Collect took %v", d)
	}
	if err == nil {
		t.Fatal("Collect error = nil")
	}
	onlyErr(t, obs, "/dev/sdh", "time limit")
	if value(t, obs, "/dev/sda", "smart.passed") != 1 {
		t.Fatal("/dev/sda did not report")
	}
}

// TestSmartCallerCancels: the agent shutting down ends Collect with
// nothing but the context's error.
func TestSmartCallerCancels(t *testing.T) {
	t.Run("before", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		obs, err := plugin(newFakeRunner(t, nil)).Collect(ctx, nil)
		if obs != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("Collect = %+v, %v", obs, err)
		}
	})
	t.Run("during", func(t *testing.T) {
		f := newFakeRunner(t, map[string]reply{"/dev/sdh": {hang: true}, "/dev/sda": {stdout: fixture(t, "ata.json")}})
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(50*time.Millisecond, cancel)
		obs, err := plugin(f).Collect(ctx, map[string]any{"devices": []string{"/dev/sda", "/dev/sdh"}})
		if obs != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("Collect = %+v, %v", obs, err)
		}
	})
}

var _ checks.Plugin = smartPlugin{}
