package testdb

import (
	"errors"
	"testing"
)

// fakeFataler is a minimal stand-in for *testing.T that records whether
// Fatal was called instead of aborting the goroutine, so fatalOnErr's
// failure branch can be exercised without failing this test binary.
type fakeFataler struct {
	failed bool
}

func (f *fakeFataler) Helper()      {}
func (f *fakeFataler) Fatal(...any) { f.failed = true }

func TestFatalOnErrCallsFatalWhenErrIsSet(t *testing.T) {
	f := &fakeFataler{}
	fatalOnErr(f, errors.New("boom"))
	if !f.failed {
		t.Error("expected Fatal to be called for a non-nil error")
	}
}

func TestFatalOnErrDoesNothingWhenErrIsNil(t *testing.T) {
	f := &fakeFataler{}
	fatalOnErr(f, nil)
	if f.failed {
		t.Error("expected Fatal not to be called for a nil error")
	}
}
