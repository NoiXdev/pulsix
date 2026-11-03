package api_test

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

const backupsPath = "/api/v1/admin/backups"

// backupAdmin returns a signed-in instance admin and their user id.
func backupAdmin(t *testing.T, s *apitest.Server) (*http.Client, uuid.UUID) {
	t.Helper()
	id := s.SeedUser("backup-admin@example.test", "correct horse battery")
	makeInstanceAdmin(t, s, id)
	return s.LoginAs(id), id
}

type backupItemDTO struct {
	Name          string  `json:"name"`
	CreatedAt     string  `json:"created_at"`
	Kind          string  `json:"kind"`
	ActorID       *string `json:"actor_id"`
	ActorName     string  `json:"actor_name"`
	PulsixVersion string  `json:"pulsix_version"`
	SizeBytes     int64   `json:"size_bytes"`
	State         string  `json:"state"`
	Error         string  `json:"error"`
}

type backupListDTO struct {
	Items            []backupItemDTO `json:"items"`
	NextRunAt        *string         `json:"next_run_at"`
	DestinationLabel string          `json:"destination_label"`
	StorageError     *string         `json:"storage_error"`
}

func listBackups(t *testing.T, s *apitest.Server, c *http.Client) backupListDTO {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, s.HTTP.URL+backupsPath, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET backups: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out backupListDTO
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return out
}

// startBackup POSTs "Back up now" and waits until the attempt has ended.
func startBackup(t *testing.T, s *apitest.Server, c *http.Client) backupItemDTO {
	t.Helper()
	resp := requestJSON(t, c, http.MethodPost, s.HTTP.URL+backupsPath, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST backups: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var started struct {
		AttemptID string `json:"attempt_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if _, err := uuid.Parse(started.AttemptID); err != nil {
		t.Fatalf("attempt_id = %q, want a uuid", started.AttemptID)
	}
	return awaitBackupEnd(t, s, c)
}

// awaitBackupEnd polls the list until no entry is running and returns the
// newest one.
func awaitBackupEnd(t *testing.T, s *apitest.Server, c *http.Client) backupItemDTO {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		list := listBackups(t, s, c)
		running := false
		for _, it := range list.Items {
			running = running || it.State == "running"
		}
		if !running && len(list.Items) > 0 {
			return list.Items[0]
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the backup did not end within 60s")
	return backupItemDTO{}
}

func TestBackupRoutesRequireAnInstanceAdmin(t *testing.T) {
	s := apitest.New(t)
	_, adminID := backupAdmin(t, s)

	member := s.SeedUser("backup-member@example.test", "correct horse battery")
	s.SeedOrg("backup-members-org", member)
	_, token := s.OrgWithToken(t)
	// A backup exists, so a refused download or delete has something to
	// protect.
	admin := s.LoginAs(adminID)
	name := startBackup(t, s, admin).Name

	settings := map[string]any{
		"destination": "folder", "s3": map[string]any{}, "enabled": false, "time_of_day": "03:00", "time_zone": "UTC",
		"retention": map[string]any{"daily": 1, "weekly": 1, "monthly": 1}, "include_metrics": true,
	}
	routes := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, backupsPath + "/settings", nil},
		{http.MethodPut, backupsPath + "/settings", settings},
		{http.MethodPost, backupsPath + "/settings/test", settings},
		{http.MethodGet, backupsPath, nil},
		{http.MethodPost, backupsPath, nil},
		{http.MethodGet, backupsPath + "/" + name + "/download", nil},
		{http.MethodDelete, backupsPath + "/" + name, nil},
	}

	for _, rt := range routes {
		who := map[string]*http.Client{
			"a plain member":      s.LoginAs(member),
			"a pending session":   s.LoginAsPending(adminID),
			"an anonymous caller": s.Client(),
		}
		for label, c := range who {
			resp := requestJSON(t, c, rt.method, s.HTTP.URL+rt.path, rt.body)
			body := mustReadBody(t, resp)
			want := http.StatusForbidden
			if label == "an anonymous caller" {
				want = http.StatusUnauthorized
			}
			if resp.StatusCode != want {
				t.Errorf("%s %s as %s: status %d, want %d (body %s)", rt.method, rt.path, label, resp.StatusCode, want, body)
			}
		}

		// A bearer token never reaches an instance admin route, whoever
		// minted it.
		resp := bearerRequestJSON(t, s, rt.method, rt.path, token, rt.body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with a bearer token: status %d, want 401 or 403", rt.method, rt.path, resp.StatusCode)
		}
	}

	// Nothing above changed anything: the backup is still listed.
	if list := listBackups(t, s, admin); len(list.Items) != 1 || list.Items[0].Name != name {
		t.Fatalf("refused calls changed the list: %+v", list.Items)
	}
}

func TestBackupSettingsRoundTrip(t *testing.T) {
	s := apitest.New(t)
	admin, adminID := backupAdmin(t, s)
	url := s.HTTP.URL + backupsPath + "/settings"

	read := func() (map[string]any, string) {
		t.Helper()
		resp := requestJSON(t, admin, http.MethodGet, url, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET settings: status %d", resp.StatusCode)
		}
		raw := mustReadBody(t, resp)
		var out map[string]any
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		return out, raw
	}

	got, _ := read()
	if got["destination"] != "folder" || got["time_of_day"] != "03:00" || got["time_zone"] != "UTC" ||
		got["enabled"] != false || got["include_metrics"] != true {
		t.Fatalf("default settings = %v", got)
	}
	if r := got["retention"].(map[string]any); r["daily"] != 7.0 || r["weekly"] != 4.0 || r["monthly"] != 6.0 {
		t.Fatalf("default retention = %v", r)
	}
	if s3 := got["s3"].(map[string]any); s3["secret_key_set"] != false {
		t.Fatalf("a fresh instance reports a stored secret: %v", s3)
	}

	const secret = "tops3cret-value-0123"
	put := map[string]any{
		"destination": "s3",
		"s3": map[string]any{
			"endpoint": "https://s3.example.test", "region": "eu-central-1", "bucket": "pulsix", "prefix": "prod/",
			"access_key": "AKIAEXAMPLE", "secret_key": secret, "path_style": true,
		},
		"enabled": true, "time_of_day": "04:30", "time_zone": "Europe/Berlin",
		"retention": map[string]any{"daily": 3, "weekly": 2, "monthly": 1}, "include_metrics": false,
	}
	if resp := requestJSON(t, admin, http.MethodPut, url, put); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT settings: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	got, raw := read()
	if strings.Contains(raw, secret) {
		t.Fatalf("GET settings leaks the secret key: %s", raw)
	}
	s3 := got["s3"].(map[string]any)
	if _, has := s3["secret_key"]; has {
		t.Fatalf("GET settings carries a secret_key field: %v", s3)
	}
	if s3["secret_key_set"] != true || s3["endpoint"] != "https://s3.example.test" || s3["region"] != "eu-central-1" ||
		s3["bucket"] != "pulsix" || s3["prefix"] != "prod/" || s3["access_key"] != "AKIAEXAMPLE" || s3["path_style"] != true {
		t.Fatalf("s3 settings = %v", s3)
	}
	if got["destination"] != "s3" || got["enabled"] != true || got["time_of_day"] != "04:30" ||
		got["time_zone"] != "Europe/Berlin" || got["include_metrics"] != false {
		t.Fatalf("settings = %v", got)
	}

	// The SPA saves what it read: GET's own body is a valid PUT body, and
	// without a secret_key it keeps the stored secret.
	if resp := requestJSON(t, admin, http.MethodPut, url, got); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT of the GET body: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if got, _ = read(); got["s3"].(map[string]any)["secret_key_set"] != true {
		t.Fatalf("a PUT without secret_key dropped the stored secret: %v", got["s3"])
	}

	// An empty secret_key removes it.
	put["s3"].(map[string]any)["secret_key"] = ""
	put["destination"] = "folder"
	if resp := requestJSON(t, admin, http.MethodPut, url, put); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT with an empty secret: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if got, _ = read(); got["s3"].(map[string]any)["secret_key_set"] != false {
		t.Fatalf("an empty secret_key kept the secret: %v", got["s3"])
	}

	ev := findEvent(t, myActivity(t, s, admin), "PUT updateBackupSettings")
	if ev.OrgID != nil || ev.ObjectType != "backup_settings" || ev.ActorID == nil || *ev.ActorID != adminID.String() {
		t.Fatalf("audit event = %+v, want an instance-level backup_settings event by the admin", ev)
	}
	for _, e := range myActivity(t, s, admin) {
		if strings.Contains(e.Summary, secret) {
			t.Fatalf("audit summary leaks the secret: %q", e.Summary)
		}
	}
}

func TestBackupSettingsRefuseInvalidInput(t *testing.T) {
	s := apitest.New(t)
	admin, _ := backupAdmin(t, s)
	url := s.HTTP.URL + backupsPath + "/settings"
	body := func(mod func(map[string]any)) map[string]any {
		m := map[string]any{
			"destination": "folder", "s3": map[string]any{}, "enabled": true, "time_of_day": "03:00", "time_zone": "UTC",
			"retention": map[string]any{"daily": 1, "weekly": 1, "monthly": 1}, "include_metrics": true,
		}
		mod(m)
		return m
	}
	cases := map[string]map[string]any{
		"time of day 25:00": body(func(m map[string]any) { m["time_of_day"] = "25:00" }),
		"unknown zone":      body(func(m map[string]any) { m["time_zone"] = "Mars/Olympus" }),
		"s3 without bucket": body(func(m map[string]any) { m["destination"] = "s3" }),
		"negative daily": body(func(m map[string]any) {
			m["retention"] = map[string]any{"daily": -1, "weekly": 1, "monthly": 1}
		}),
	}
	for label, in := range cases {
		put := requestJSON(t, admin, http.MethodPut, url, in)
		if put.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("PUT with %s: status %d, want 422 (body %s)", label, put.StatusCode, mustReadBody(t, put))
		}
		test := requestJSON(t, admin, http.MethodPost, url+"/test", in)
		if test.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("test with %s: status %d, want 422 (body %s)", label, test.StatusCode, mustReadBody(t, test))
		}
	}
	// Nothing was saved.
	resp := requestJSON(t, admin, http.MethodGet, url, nil)
	var got struct {
		TimeOfDay string `json:"time_of_day"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || got.TimeOfDay != "03:00" {
		t.Fatalf("refused PUTs changed the settings: %+v, %v", got, err)
	}
}

func TestBackupSettingsTest(t *testing.T) {
	s := apitest.New(t)
	admin, _ := backupAdmin(t, s)
	url := s.HTTP.URL + backupsPath + "/settings/test"
	folder := map[string]any{
		"destination": "folder", "s3": map[string]any{}, "enabled": false, "time_of_day": "03:00", "time_zone": "UTC",
		"retention": map[string]any{"daily": 1, "weekly": 1, "monthly": 1}, "include_metrics": true,
	}
	if resp := requestJSON(t, admin, http.MethodPost, url, folder); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("test of the folder: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// A folder that cannot be written to fails the probe, and the detail
	// says why.
	if err := os.RemoveAll(s.BackupDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.BackupDir, []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp := requestJSON(t, admin, http.MethodPost, url, folder)
	raw := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(raw, "backup folder") {
		t.Fatalf("test of a broken folder: status %d, want 422 naming the folder; body %s", resp.StatusCode, raw)
	}

	// The probe is not an action worth an audit record.
	for _, e := range myActivity(t, s, admin) {
		if strings.Contains(e.Action, "Backup") {
			t.Fatalf("testing the destination was audited: %+v", e)
		}
	}
}
