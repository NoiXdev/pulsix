// Table-driven tests for the agent's configuration loading. The defaults
// and clamps are the plan's contract; the manager-addr rules mirror the
// worker's (host:port, schemes rejected by name).
package agent

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		t.Skipf("no usable OS hostname: %v", err)
	}
	tests := []struct {
		name    string
		env     []string
		want    Config
		wantErr string
	}{
		{
			name:    "manager addr required",
			env:     nil,
			wantErr: "PULSIX_MANAGER_ADDR is required",
		},
		{
			name:    "scheme rejected by name",
			env:     []string{"PULSIX_MANAGER_ADDR=https://manager.example:8444"},
			wantErr: `must be host:port — write it without the "https" scheme`,
		},
		{
			name:    "grpc scheme rejected by name",
			env:     []string{"PULSIX_MANAGER_ADDR=grpcs://manager.example:8444"},
			wantErr: `must be host:port — write it without the "grpcs" scheme`,
		},
		{
			name:    "no port",
			env:     []string{"PULSIX_MANAGER_ADDR=manager.example"},
			wantErr: "must be host:port",
		},
		{
			name:    "no host",
			env:     []string{"PULSIX_MANAGER_ADDR=:8444"},
			wantErr: "host and port are both required",
		},
		{
			name:    "no port after colon",
			env:     []string{"PULSIX_MANAGER_ADDR=manager.example:"},
			wantErr: "host and port are both required",
		},
		{
			name: "defaults",
			env:  []string{"PULSIX_MANAGER_ADDR=manager.example:8444"},
			want: Config{
				ManagerAddr:       "manager.example:8444",
				AgentName:         hostname,
				DataDir:           "/etc/pulsix",
				HeartbeatInterval: 30 * time.Second,
				FactsInterval:     6 * time.Hour,
				CertPath:          "/etc/pulsix/agent.crt",
				KeyPath:           "/etc/pulsix/agent.key",
				MetaPath:          "/etc/pulsix/agent.json",
			},
		},
		{
			name: "all optional values set",
			env: []string{
				"PULSIX_MANAGER_ADDR=manager.example:8444",
				"PULSIX_AGENT_NAME=staging-node-1",
				"PULSIX_DATA_DIR=/var/lib/pulsix-agent",
				"PULSIX_MANAGER_SERVER_NAME=pulsix-manager",
				"PULSIX_HEARTBEAT_INTERVAL=1m",
				"PULSIX_FACTS_INTERVAL=12h",
				"PULSIX_ENROLL_TOKEN=  pxa1.abc.def.ghi  ",
			},
			want: Config{
				ManagerAddr:       "manager.example:8444",
				ServerName:        "pulsix-manager",
				AgentName:         "staging-node-1",
				DataDir:           "/var/lib/pulsix-agent",
				HeartbeatInterval: time.Minute,
				FactsInterval:     12 * time.Hour,
				EnrollToken:       "pxa1.abc.def.ghi",
				CertPath:          "/var/lib/pulsix-agent/agent.crt",
				KeyPath:           "/var/lib/pulsix-agent/agent.key",
				MetaPath:          "/var/lib/pulsix-agent/agent.json",
			},
		},
		{
			name: "heartbeat clamped low",
			env: []string{
				"PULSIX_MANAGER_ADDR=manager.example:8444",
				"PULSIX_HEARTBEAT_INTERVAL=100ms",
			},
			want: Config{HeartbeatInterval: 5 * time.Second},
		},
		{
			name: "heartbeat clamped high",
			env: []string{
				"PULSIX_MANAGER_ADDR=manager.example:8444",
				"PULSIX_HEARTBEAT_INTERVAL=1h",
			},
			want: Config{HeartbeatInterval: 10 * time.Minute},
		},
		{
			name: "facts clamped low",
			env: []string{
				"PULSIX_MANAGER_ADDR=manager.example:8444",
				"PULSIX_FACTS_INTERVAL=1s",
			},
			want: Config{FactsInterval: 30 * time.Minute},
		},
		{
			name: "facts clamped high",
			env: []string{
				"PULSIX_MANAGER_ADDR=manager.example:8444",
				"PULSIX_FACTS_INTERVAL=48h",
			},
			want: Config{FactsInterval: 24 * time.Hour},
		},
		{
			name: "unparsable heartbeat",
			env: []string{
				"PULSIX_MANAGER_ADDR=manager.example:8444",
				"PULSIX_HEARTBEAT_INTERVAL=soon",
			},
			wantErr: "PULSIX_HEARTBEAT_INTERVAL",
		},
		{
			name: "unparsable facts interval",
			env: []string{
				"PULSIX_MANAGER_ADDR=manager.example:8444",
				"PULSIX_FACTS_INTERVAL=3 bananas",
			},
			wantErr: "PULSIX_FACTS_INTERVAL",
		},
		{
			name: "agent name over the certificate's bound",
			env: []string{
				"PULSIX_MANAGER_ADDR=manager.example:8444",
				"PULSIX_AGENT_NAME=" + strings.Repeat("x", 65),
			},
			wantErr: "PULSIX_AGENT_NAME",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(tt.env)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			// Only compare the fields the case pins: a zero want means
			// "unpinned" (interval-only cases inherit the hostname
			// default, clamped cases keep the derived paths, ...).
			pin := func(got, want string, msg string) {
				t.Helper()
				if want != "" && got != want {
					t.Errorf("%s = %q, want %q", msg, got, want)
				}
			}
			pin(got.ManagerAddr, tt.want.ManagerAddr, "ManagerAddr")
			pin(got.AgentName, tt.want.AgentName, "AgentName")
			pin(got.DataDir, tt.want.DataDir, "DataDir")
			pin(got.ServerName, tt.want.ServerName, "ServerName")
			pin(got.CertPath, tt.want.CertPath, "CertPath")
			pin(got.KeyPath, tt.want.KeyPath, "KeyPath")
			pin(got.MetaPath, tt.want.MetaPath, "MetaPath")
			if got.EnrollToken != tt.want.EnrollToken {
				t.Errorf("EnrollToken = %q, want %q", got.EnrollToken, tt.want.EnrollToken)
			}
			if tt.want.HeartbeatInterval != 0 && got.HeartbeatInterval != tt.want.HeartbeatInterval {
				t.Errorf("HeartbeatInterval = %v, want %v", got.HeartbeatInterval, tt.want.HeartbeatInterval)
			}
			if tt.want.FactsInterval != 0 && got.FactsInterval != tt.want.FactsInterval {
				t.Errorf("FactsInterval = %v, want %v", got.FactsInterval, tt.want.FactsInterval)
			}
		})
	}
}

func TestLoadInvalidTokenIsKeptVerbatim(t *testing.T) {
	// Config keeps the token optional and unparsed: whether one is needed
	// is the loop's decision (it depends on the data dir's contents), so a
	// garbage token must pass through unchanged — the enrollment itself
	// reports it against the manager's own parser.
	env := []string{"PULSIX_MANAGER_ADDR=manager.example:8444", "PULSIX_ENROLL_TOKEN=not-a-token"}
	cfg, err := Load(env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EnrollToken != "not-a-token" {
		t.Fatalf("EnrollToken = %q, want the value verbatim", cfg.EnrollToken)
	}
}
