package grpcsrv

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/varsource"
)

// TestReportResultCarriesVaultNames: the vault names a current worker puts
// in its sync result's variable sources report (backlog 2.134) reach the
// result recorder unchanged; a report without them stays without (an
// older worker's), never an empty list that would read as "none".
func TestReportResultCarriesVaultNames(t *testing.T) {
	vault := &varsource.Vault{
		Names:          []varsource.VaultName{{Name: "db_password", File: "group_vars/all.yml"}},
		EncryptedFiles: []string{"group_vars/all/vault.yml"}, Unread: []string{}, Cut: false,
	}
	for _, tc := range []struct {
		name  string
		vault *varsource.Vault
	}{{"current worker", vault}, {"older worker", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			certPEM, keyPEM, _ := h.issue("fra-1-a", orgID, zoneID, true)
			client := h.client(certPEM, keyPEM)

			report, _, err := varsource.Encode(varsource.Report{Commit: "abc", Playbooks: []varsource.Playbook{}, Notes: []varsource.Note{}, Vault: tc.vault}, varsource.MaxReportBytes)
			if err != nil {
				t.Fatal(err)
			}
			stats, err := json.Marshal(map[string]any{"repo_id": "r", "var_sources": json.RawMessage(report)})
			if err != nil {
				t.Fatal(err)
			}
			jobID := uuid.Must(uuid.NewV7())
			if _, err := client.ReportResult(t.Context(), &workerpb.ReportResultRequest{
				JobId: jobID.String(), Status: "succeeded", Stats: string(stats),
			}); err != nil {
				t.Fatal(err)
			}
			results := h.results.Results()
			if len(results) != 1 {
				t.Fatalf("results = %d, want 1", len(results))
			}
			raw, err := json.Marshal(results[0].Stats["var_sources"])
			if err != nil {
				t.Fatal(err)
			}
			var got varsource.Report
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Vault, tc.vault) {
				t.Fatalf("vault = %+v, want %+v", got.Vault, tc.vault)
			}
		})
	}
}
