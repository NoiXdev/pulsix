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
	r, err := New().(*smartPlugin).smartctl()
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
	wantArgv := []string{"--json", "-a", "-n", "standby,0", "-d", "sat", "/dev/sda"}
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
	deviceCount(t, obs, 3)
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
			if n := len(itemObs(obs, "/dev/sdc")); n != 6 {
				t.Errorf("got %d observations for /dev/sdc, want 6: %+v", n, obs)
			}
			deviceCount(t, obs, 1)
		})
	}
}

// TestSmartStandbyReportsNothing: -n standby,0 makes smartctl leave a
// sleeping disk alone and exit 0 with JSON that carries no data blocks.
// That disk reports nothing this round — no reading, no error — and the
// others still report.
func TestSmartStandbyReportsNothing(t *testing.T) {
	f := newFakeRunner(t, map[string]reply{
		"/dev/sdd": {stdout: fixture(t, "standby.json")},
		"/dev/sda": {stdout: fixture(t, "ata.json")},
	})
	obs, err := plugin(f).Collect(context.Background(), map[string]any{"devices": []any{"/dev/sdd", "/dev/sda"}})
	if err != nil {
		t.Fatalf("Collect error = %v", err)
	}
	for _, c := range f.callArgs() {
		if i := slices.Index(c, "-n"); i < 0 || c[i+1] != "standby,0" {
			t.Errorf("argv %q: want -n standby,0", c)
		}
	}
	if got := itemObs(obs, "/dev/sdd"); len(got) != 0 {
		t.Errorf("standby disk reported %+v, want nothing", got)
	}
	if got := value(t, obs, "/dev/sda", "smart.passed"); got != 1 {
		t.Errorf("/dev/sda passed = %v", got)
	}
	deviceCount(t, obs, 2)
}

// TestSmartBitOneIsAlwaysAnError: with -n standby,0 a sleeping disk never
// sets bit 1, so bit 1 is "device not opened" whatever the messages say.
func TestSmartBitOneIsAlwaysAnError(t *testing.T) {
	for _, stdout := range [][]byte{
		[]byte(`{"smartctl":{"messages":[{"string":"Device is in STANDBY mode, exit(2)","severity":"information"}],"exit_status":2}}`),
		[]byte("Device is in STANDBY mode, exit(2)\n"),
	} {
		f := newFakeRunner(t, map[string]reply{"/dev/sdd": {stdout: stdout, exit: 2}})
		obs, err := plugin(f).Collect(context.Background(), map[string]any{"devices": []string{"/dev/sdd"}})
		if err == nil {
			t.Fatalf("%s: Collect error = nil", stdout)
		}
		onlyErr(t, obs, "/dev/sdd", "could not open the device")
	}
}

// TestSmartNoSMARTBlocksReportsNothing is Review Focus 1: valid JSON
// without the blocks the metrics come from (a USB stick, a virtual disk)
// is not a failure of that device. The device reports only what it states,
// here nothing; the host-level count still says it was found, as long as
// another device has SMART data or is asleep.
func TestSmartNoSMARTBlocksReportsNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		exit int
	}{
		{"usb-no-smart.json", 4},
		{"virtual-disk.json", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRunner(t, map[string]reply{
				"/dev/sdx": {stdout: fixture(t, tc.name), exit: tc.exit},
				"/dev/sda": {stdout: fixture(t, "ata.json")},
			})
			obs, err := plugin(f).Collect(context.Background(), map[string]any{"devices": []string{"/dev/sdx", "/dev/sda"}})
			if err != nil {
				t.Fatalf("Collect error = %v", err)
			}
			if got := itemObs(obs, "/dev/sdx"); len(got) != 0 {
				t.Fatalf("/dev/sdx reported %+v, want nothing", got)
			}
			deviceCount(t, obs, 2)
		})
	}
}

// TestSmartNoDeviceWithSMARTData: a virtual machine's disks (virtio-scsi
// shows them as QEMU disks without SMART) answer with valid JSON and no
// readings. Reporting nothing at all would leave the check silent, so
// when no device has SMART data and none is asleep, smart.devices carries
// the error instead of the count and nothing else is reported.
func TestSmartNoDeviceWithSMARTData(t *testing.T) {
	vmScan := []byte(`{"devices":[{"name":"/dev/sda","type":"scsi"},{"name":"/dev/sdb","type":"scsi"}]}`)
	noData := []byte(`{"smartctl":{"exit_status":0},"device":{"name":"/dev/sdx","type":"sat"},"smart_support":{"available":true,"enabled":false}}`)
	for _, tc := range []struct {
		desc    string
		params  map[string]any
		replies map[string]reply
	}{
		{"virtio-scsi disks", nil, map[string]reply{
			"scan":             {stdout: vmScan},
			"/dev/sda -d scsi": {stdout: fixture(t, "virtual-disk.json")},
			"/dev/sdb -d scsi": {stdout: fixture(t, "virtual-disk.json")},
		}},
		{"USB stick without SMART", map[string]any{"devices": []string{"/dev/sdx"}}, map[string]reply{
			"/dev/sdx": {stdout: fixture(t, "usb-no-smart.json"), exit: 4},
		}},
		{"SMART disabled, no data blocks", map[string]any{"devices": []string{"/dev/sdx"}}, map[string]reply{
			"/dev/sdx": {stdout: noData},
		}},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			obs, err := plugin(newFakeRunner(t, tc.replies)).Collect(context.Background(), tc.params)
			if err == nil || !strings.Contains(err.Error(), "no device with SMART data") {
				t.Fatalf("Collect error = %v, want no device with SMART data", err)
			}
			if len(obs) != 1 {
				t.Fatalf("observations = %+v, want only the error on smart.devices", obs)
			}
			hostErr(t, obs, "no device with SMART data")
			encodesAsCheckResult(t, obs[0])
		})
	}
}

// TestSmartStandbyIsNotNoSMART: a disk in standby answers without a
// smart_support block (smartctl stops before identifying it); a disk
// without SMART answers with one that says unavailable. Asleep is not a
// fault, so a host whose disks all sleep reports the count and nothing
// else, and one sleeping disk is enough to keep the error away.
func TestSmartStandbyIsNotNoSMART(t *testing.T) {
	for _, tc := range []struct {
		desc    string
		replies map[string]reply
		want    float64
	}{
		{"all asleep", map[string]reply{
			"/dev/sda": {stdout: fixture(t, "standby.json")},
			"/dev/sdb": {stdout: fixture(t, "standby.json")},
		}, 2},
		{"asleep and virtual", map[string]reply{
			"/dev/sda": {stdout: fixture(t, "standby.json")},
			"/dev/sdb": {stdout: fixture(t, "virtual-disk.json")},
		}, 2},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			obs, err := plugin(newFakeRunner(t, tc.replies)).Collect(context.Background(), map[string]any{"devices": []string{"/dev/sda", "/dev/sdb"}})
			if err != nil {
				t.Fatalf("Collect error = %v", err)
			}
			if len(obs) != 1 {
				t.Fatalf("observations = %+v, want only smart.devices", obs)
			}
			deviceCount(t, obs, tc.want)
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
	if len(itemObs(obs, "/dev/sdx")) != 3 {
		t.Fatalf("got %+v, want temperature, pending sectors and media errors only", obs)
	}
	deviceCount(t, obs, 1)
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
	if len(itemObs(obs, "/dev/sdx")) != 1 || value(t, obs, "/dev/sdx", "smart.temperature_celsius") != 31 {
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
			deviceCount(t, obs, 2)
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
	if len(obs) != 1 || obs[0].Item != "" || obs[0].Metric != "smart.devices" || obs[0].Err != "smartctl not found; install smartmontools" {
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
			if len(obs) != 1 || obs[0].Item != "" || obs[0].Metric != "smart.devices" || !strings.Contains(obs[0].Err, tc.want) {
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
	deviceCount(t, obs, 2)
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
	deviceCount(t, obs, float64(len(good)))
	for _, o := range obs {
		encodesAsCheckResult(t, o)
	}
}

// TestSmartBadScanType: a device type that is not a plain smartctl type
// name is not passed on; "/" after the first character is (areca, hpt).
func TestSmartBadScanType(t *testing.T) {
	scan := []byte(`{"devices":[
		{"name":"/dev/sda","type":"-x"},
		{"name":"/dev/sdb","type":"sat\u0000"},
		{"name":"/dev/sdd","type":"/sat"},
		{"name":"/dev/sdc","type":"sat,12"},
		{"name":"/dev/arcmsr0","type":"areca,3/1"},
		{"name":"/dev/sde","type":"hpt,1/1/2"}
	]}`)
	f := newFakeRunner(t, map[string]reply{
		"scan":                      {stdout: scan},
		"/dev/sdc -d sat,12":        {stdout: fixture(t, "ata.json")},
		"/dev/arcmsr0 -d areca,3/1": {stdout: fixture(t, "ata.json")},
		"/dev/sde -d hpt,1/1/2":     {stdout: fixture(t, "ata.json")},
	})
	obs, err := plugin(f).Collect(context.Background(), nil)
	for _, n := range []string{"/dev/sda", "/dev/sdb", "/dev/sdd"} {
		if err == nil || !strings.Contains(err.Error(), n) {
			t.Fatalf("Collect error = %v, want %s named", err, n)
		}
	}
	want := []string{"/dev/arcmsr0 [areca,3/1]", "/dev/sdc", "/dev/sde [hpt,1/1/2]"}
	if got := items(obs); !slices.Equal(got, want) {
		t.Fatalf("items = %q, want %q", got, want)
	}
}

// TestSmartControllerDiskItems: a disk behind a RAID controller (its -d
// type's family names one) is "<name> [<type>]"; any other disk is its
// name. The item depends on the entry alone.
func TestSmartControllerDiskItems(t *testing.T) {
	for _, tc := range []struct{ typ, want string }{
		{"megaraid,0", "/dev/x [megaraid,0]"},
		{"MegaRAID,4", "/dev/x [MegaRAID,4]"},
		{"sat+megaraid,2", "/dev/x [sat+megaraid,2]"},
		{"cciss,1", "/dev/x [cciss,1]"},
		{"3ware,0", "/dev/x [3ware,0]"},
		{"areca,3/1", "/dev/x [areca,3/1]"},
		{"aacraid,0,0,1", "/dev/x [aacraid,0,0,1]"},
		{"sssraid,0,1", "/dev/x [sssraid,0,1]"},
		{"hpt,1/1", "/dev/x [hpt,1/1]"},
		{"sat", "/dev/x"},
		{"sat,12", "/dev/x"},
		{"nvme", "/dev/x"},
		{"usbjmicron,0", "/dev/x"},
		{"", "/dev/x"},
	} {
		if got := scanItem("/dev/x", tc.typ); got != tc.want {
			t.Errorf("scanItem(%q) = %q, want %q", tc.typ, got, tc.want)
		}
	}
}

// TestSmartControllerItemsAreStable: a controller disk alone gets the
// suffix too, and siblings coming or going do not rename it.
func TestSmartControllerItemsAreStable(t *testing.T) {
	scanOf := func(types ...string) []byte {
		var devs []map[string]string
		for _, typ := range types {
			devs = append(devs, map[string]string{"name": "/dev/bus/0", "type": typ})
		}
		b, _ := json.Marshal(map[string]any{"devices": devs})
		return b
	}
	for _, types := range [][]string{{"megaraid,1"}, {"megaraid,0", "megaraid,1"}, {"megaraid,1", "megaraid,2", "megaraid,1"}} {
		replies := map[string]reply{"scan": {stdout: scanOf(types...)}}
		for _, typ := range types {
			replies["/dev/bus/0 -d "+typ] = reply{stdout: fixture(t, "ata.json")}
		}
		f := newFakeRunner(t, replies)
		obs, err := plugin(f).Collect(context.Background(), nil)
		if err != nil {
			t.Fatalf("%q: Collect error = %v", types, err)
		}
		if got := value(t, obs, "/dev/bus/0 [megaraid,1]", "smart.passed"); got != 1 {
			t.Fatalf("%q: megaraid,1 passed = %v", types, got)
		}
		if n, want := len(f.callArgs()), 1+len(slices.Compact(slices.Sorted(slices.Values(types)))); n != want {
			t.Errorf("%q: %d smartctl calls, want %d (a repeated entry once)", types, n, want)
		}
	}
}

// TestSmartRepeatedScanNamesStayDistinct: disks behind one RAID controller
// share a device name and differ by type; each keeps its own item. Two
// entries that would share an item otherwise do not collide silently: the
// later one is left out and named.
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

	scan := []byte(`{"devices":[{"name":"/dev/sda","type":"sat"},{"name":"/dev/sda","type":"scsi"}]}`)
	f = newFakeRunner(t, map[string]reply{"scan": {stdout: scan}, "/dev/sda -d sat": {stdout: fixture(t, "ata.json")}})
	obs, err = plugin(f).Collect(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), `"scsi"`) {
		t.Fatalf("Collect error = %v, want the -d scsi entry named", err)
	}
	if got := items(obs); !slices.Equal(got, []string{"/dev/sda"}) || value(t, obs, "/dev/sda", "smart.passed") != 1 {
		t.Fatalf("observations = %+v", obs)
	}
}

// TestSmartEveryDeviceRefused: when no device is left to query, the
// reasons show on item "" instead of the check reporting nothing.
func TestSmartEveryDeviceRefused(t *testing.T) {
	for _, tc := range []struct {
		desc   string
		params map[string]any
		scan   []byte
		want   []string
	}{
		{"configured", map[string]any{"devices": []string{"-x", "/dev/a\nb"}}, nil, []string{`"-x"`, `"/dev/a\nb"`}},
		{"scanned", nil, []byte(`{"devices":[{"name":"-x"},{"name":"/dev/sda","type":"-d"}]}`), []string{`"-x"`, `"/dev/sda"`}},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			f := newFakeRunner(t, map[string]reply{"scan": {stdout: tc.scan}})
			obs, err := plugin(f).Collect(context.Background(), tc.params)
			if err == nil {
				t.Fatal("Collect error = nil")
			}
			if len(obs) != 1 || obs[0].Item != "" || obs[0].Metric != "smart.devices" {
				t.Fatalf("observations = %+v, want one error on item \"\"", obs)
			}
			for _, w := range tc.want {
				if !strings.Contains(obs[0].Err, w) {
					t.Errorf("error %q does not name %s", obs[0].Err, w)
				}
			}
			encodesAsCheckResult(t, obs[0])
		})
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
	deviceCount(t, obs, 8) // the failures are on the devices' items
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

var _ checks.StatefulPlugin = (*smartPlugin)(nil)

// TestSmartDevicesCountsOnlyQueriedDevices: smart.devices counts the
// devices found or configured that were not refused; a refused entry is
// named in the advisory error and not counted.
func TestSmartDevicesCountsOnlyQueriedDevices(t *testing.T) {
	f := newFakeRunner(t, map[string]reply{"/dev/sda": {stdout: fixture(t, "ata.json")}})
	obs, err := plugin(f).Collect(context.Background(), map[string]any{"devices": []string{"-x", "/dev/sda"}})
	if err == nil || !strings.Contains(err.Error(), `"-x"`) {
		t.Fatalf("Collect error = %v, want the refused entry named", err)
	}
	deviceCount(t, obs, 1)
}
