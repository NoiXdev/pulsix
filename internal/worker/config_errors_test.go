// Load: every environment value the worker refuses, and the message each
// refusal carries.
package worker

import (
	"strings"
	"testing"
)

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name   string
		env    map[string]string
		inName string // error text must name this value
	}{
		{
			name: "missing manager addr",
			env: map[string]string{
				"PULSIX_WORKER_CERT": "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
			},
			inName: "PULSIX_MANAGER_ADDR",
		},
		{
			name: "empty manager addr",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
			},
			inName: "PULSIX_MANAGER_ADDR",
		},
		{
			name: "http scheme addr",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "http://manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
			},
			inName: "http://manager.example.test:8444",
		},
		{
			name: "grpc scheme addr",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "grpcs://manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
			},
			inName: "grpcs://manager.example.test:8444",
		},
		{
			name: "addr without port",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
			},
			inName: "manager.example.test",
		},
		{
			name: "missing cert path",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_KEY":   "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
			},
			inName: "PULSIX_WORKER_CERT",
		},
		{
			name: "missing key path",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_CA_CERT": "/ca.pem",
			},
			inName: "PULSIX_WORKER_KEY",
		},
		{
			name: "missing CA path",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem",
			},
			inName: "PULSIX_CA_CERT",
		},
		{
			name: "invalid zone id",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
				"PULSIX_WORKER_ZONES": "not-a-uuid",
			},
			inName: "not-a-uuid",
		},
		{
			name: "second zone invalid",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
				"PULSIX_WORKER_ZONES": "018f4e2a-7b3c-7d4e-8f90-1a2b3c4d5e61,broken",
			},
			inName: "broken",
		},
		{
			name: "invalid claim interval",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
				"PULSIX_CLAIM_INTERVAL": "soon",
			},
			inName: "PULSIX_CLAIM_INTERVAL",
		},
		{
			name: "negative claim interval",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
				"PULSIX_CLAIM_INTERVAL": "-5s",
			},
			inName: "PULSIX_CLAIM_INTERVAL",
		},
		{
			// A bare name is resolved by exec.LookPath against PATH, so the
			// environment would choose which setuid-root binary the worker
			// execs. Refused at load, not discovered at run time.
			name: "relative setuid helper",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
				"PULSIX_WORKER_SETUID_HELPER": "pulsix-setuid-exec",
			},
			inName: "PULSIX_WORKER_SETUID_HELPER",
		},
		{
			name: "dot-relative setuid helper",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
				"PULSIX_WORKER_SETUID_HELPER": "./setuid-exec",
			},
			inName: "./setuid-exec",
		},
		{
			name: "invalid max concurrent",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
				"PULSIX_MAX_CONCURRENT": "many",
			},
			inName: "PULSIX_MAX_CONCURRENT",
		},
		{
			name: "zero max concurrent",
			env: map[string]string{
				"PULSIX_MANAGER_ADDR": "manager.example.test:8444",
				"PULSIX_WORKER_CERT":  "/c.pem", "PULSIX_WORKER_KEY": "/k.pem", "PULSIX_CA_CERT": "/ca.pem",
				"PULSIX_MAX_CONCURRENT": "0",
			},
			inName: "PULSIX_MAX_CONCURRENT",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(envSlice(tc.env))
			if err == nil {
				t.Fatal("Load must fail")
			}
			if !strings.Contains(err.Error(), tc.inName) {
				t.Fatalf("error %q must name %q", err, tc.inName)
			}
		})
	}
}
