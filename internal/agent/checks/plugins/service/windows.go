//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// scmLister asks the Windows service manager about each service. It opens
// the manager with connect rights only and each service with query-status
// rights only (svc/mgr would ask for full access).
type scmLister struct{}

func newUnitLister() unitLister { return scmLister{} }

func (scmLister) States(ctx context.Context, units []string) (map[string]unitResult, error) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, fmt.Errorf("open service manager: %w", err)
	}
	defer windows.CloseServiceHandle(m) //nolint:errcheck // nothing to do on a failed close

	out := make(map[string]unitResult, len(units))
	for _, u := range units {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out[u] = queryService(m, u)
	}
	return out, nil
}

func queryService(m windows.Handle, name string) unitResult {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return unitResult{Err: fmt.Errorf("service name %q: %w", name, err)}
	}
	h, err := windows.OpenService(m, p, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return unitResult{Label: "unknown"}
		}
		return unitResult{Err: fmt.Errorf("open service %q: %w", name, err)}
	}
	defer windows.CloseServiceHandle(h) //nolint:errcheck // nothing to do on a failed close

	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(h, &st); err != nil {
		return unitResult{Err: fmt.Errorf("query service %q: %w", name, err)}
	}
	return unitResult{Label: windowsLabel(st.CurrentState, st.Win32ExitCode)}
}
