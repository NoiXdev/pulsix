package checks

import "testing"

func TestWindowsLabel(t *testing.T) {
	cases := []struct {
		name        string
		state, exit uint32
		want        string
	}{
		{"running", 4, 0, "active"},
		{"start pending", 2, 0, "activating"},
		{"continue pending", 5, 0, "activating"},
		{"stopped clean", 1, 0, "inactive"},
		{"stopped never started since boot", 1, 1077, "inactive"},
		{"stopped with error", 1, 5, "failed"},
		{"stopped with 1066", 1, 1066, "failed"},
		{"stopped service-specific", 1, 1064, "failed"},
		{"stop pending", 3, 0, "unknown"},
		{"pause pending", 6, 0, "unknown"},
		{"paused", 7, 0, "unknown"},
		{"garbage state", 99, 0, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowsLabel(tc.state, tc.exit); got != tc.want {
				t.Errorf("windowsLabel(%d, %d) = %q, want %q", tc.state, tc.exit, got, tc.want)
			}
		})
	}
}

const launchctlFixture = "PID\tStatus\tLabel\n123\t0\tcom.a\n-\t0\tcom.b\n-\t78\tcom.c\n-\t-9\tcom.d\nbroken line\n"

func TestParseLaunchctlList(t *testing.T) {
	jobs, err := parseLaunchctlList([]byte(launchctlFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(jobs) != 4 {
		t.Fatalf("got %d jobs, want 4: %+v", len(jobs), jobs)
	}
	if got := jobs["com.a"]; got != (launchdJob{PID: "123", Status: "0"}) {
		t.Errorf("com.a = %+v", got)
	}
	for name, in := range map[string]string{
		"missing header": "123\t0\tcom.a\n",
		"empty":          "",
		"blank":          "\n\n",
	} {
		if _, err := parseLaunchctlList([]byte(in)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestLaunchdLabel(t *testing.T) {
	jobs, err := parseLaunchctlList([]byte(launchctlFixture))
	if err != nil {
		t.Fatal(err)
	}
	for label, want := range map[string]string{
		"com.a": "active", "com.b": "inactive", "com.c": "failed", "com.d": "failed", "com.absent": "unknown",
	} {
		job, found := jobs[label]
		if got := launchdLabel(job, found); got != want {
			t.Errorf("launchdLabel(%s) = %q, want %q", label, got, want)
		}
	}
}
