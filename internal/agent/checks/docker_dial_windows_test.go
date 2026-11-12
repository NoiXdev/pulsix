//go:build windows

package checks

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Microsoft/go-winio"
)

func TestClientOverNamedPipe(t *testing.T) {
	name := fmt.Sprintf("pulsix-test-%d-%d", os.Getpid(), os.Getpid()^0x5bd1)
	l, err := winio.ListenPipe(`\\.\pipe\`+name, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/json" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"Id":"aaaaaaaaaaaaaaaa","Names":["/web"],"State":"running"}]`))
	}))
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)

	e, err := newDockerEngine("npipe:////./pipe/" + name)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "web" {
		t.Errorf("got %+v", got)
	}
}
