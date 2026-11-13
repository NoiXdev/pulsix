package webchecktest_test

import (
	"errors"
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck/webchecktest"
)

func TestServerRecordsAndAnswers(t *testing.T) {
	srv := webchecktest.NewServer(t, "x")
	srv.Set(404, "y")
	if len(srv.Auths()) != 0 || len(srv.URIs()) != 0 {
		t.Fatal("requests recorded before any call")
	}
}

func TestMustDownSet(t *testing.T) {
	v := 0.0
	obs := []checks.Observation{
		{Metric: "x.up", Value: &v},
		checks.ErrObservation("", "x.a", errors.New("x: boom")),
	}
	webchecktest.MustDown(t, obs, "x", "x.up", []string{"x.a"}, "boom")
	webchecktest.MustErr(t, obs, "x", "x.a", "boom")
	webchecktest.MustAbsent(t, obs, "x.b")
	webchecktest.NoSecretIn(t, webchecktest.NewServer(t, ""), obs, nil)
}
