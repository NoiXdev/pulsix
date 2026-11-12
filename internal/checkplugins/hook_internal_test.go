package checkplugins

import (
	"errors"
	"testing"
)

// TestValidateRunsTheCheckHook: no plugin needs a Check hook today, so this
// pins its contract on a descriptor of its own — it runs only after every
// parameter passed its spec, and its refusal is prefixed with the plugin's
// name and wraps ErrInvalidParams.
func TestValidateRunsTheCheckHook(t *testing.T) {
	var ran int
	d := Descriptor{
		Name:   "probe",
		Params: []ParamSpec{{Name: "a", Type: TypeBool}, {Name: "b", Type: TypeBool}},
		Check: func(params map[string]any) error {
			ran++
			if params["a"] == true && params["b"] == true {
				return errors.New("a and b exclude each other")
			}
			return nil
		},
	}
	if err := d.validate(map[string]any{"a": true}); err != nil || ran != 1 {
		t.Fatalf("validate(a) = %v after %d hook runs, want nil after 1", err, ran)
	}
	err := d.validate(map[string]any{"a": true, "b": true})
	if !errors.Is(err, ErrInvalidParams) || err.Error() != "probe: a and b exclude each other" {
		t.Fatalf("validate(a, b) = %v, want ErrInvalidParams \"probe: a and b exclude each other\"", err)
	}
	ran = 0
	if err := d.validate(map[string]any{"a": "yes"}); err == nil || ran != 0 {
		t.Fatalf("validate(a: yes) = %v after %d hook runs, want a refusal before the hook", err, ran)
	}
}

// TestParamProblemRefusesWhatNoValidatorKnows: a spec with an unknown type
// or format accepts nothing rather than everything.
func TestParamProblemRefusesWhatNoValidatorKnows(t *testing.T) {
	if (ParamSpec{Name: "x", Type: "object"}).problem("v") == "" {
		t.Fatal("an unknown type accepted a value")
	}
	if (ParamSpec{Name: "x", Type: TypeString, Format: "nope"}).problem("v") == "" {
		t.Fatal("an unknown format accepted a value")
	}
}

// TestDescribeInteger covers the bounds no descriptor uses today.
func TestDescribeInteger(t *testing.T) {
	for _, tc := range []struct {
		spec ParamSpec
		want string
	}{
		{ParamSpec{Min: bound(1), Max: bound(2)}, "a whole number between 1 and 2"},
		{ParamSpec{Min: bound(1)}, "a whole number of at least 1"},
		{ParamSpec{Max: bound(2)}, "a whole number of at most 2"},
		{ParamSpec{}, "a whole number"},
	} {
		if got := tc.spec.describeInteger(); got != tc.want {
			t.Errorf("describeInteger(%+v) = %q, want %q", tc.spec, got, tc.want)
		}
	}
}
