package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
)

// ruleAnyKeyDTO is the slice of a rule's read shape these tests need,
// item_check_key included (backlog 3.13e).
type ruleAnyKeyDTO struct {
	ID           string `json:"id"`
	Scope        string `json:"scope"`
	Item         string `json:"item"`
	ItemCheckKey string `json:"item_check_key"`
	CheckKey     string `json:"check_key"`
	Metric       string `json:"metric"`
}

func itemDiskRuleBody(host, itemCheckKey string) map[string]any {
	body := map[string]any{
		"name": "data disk", "scope": "item", "host_id": host, "item": "/data",
		"metric": "disk.used_pct", "severity": "warn", "mode": "form",
		"form": map[string]any{"metric": "disk.used_pct", "op": ">", "value": 70},
	}
	if itemCheckKey != "" {
		body["item_check_key"] = itemCheckKey
	}
	return body
}

func decodeRule(t *testing.T, resp *http.Response) ruleAnyKeyDTO {
	t.Helper()
	var out struct {
		Rule ruleAnyKeyDTO `json:"rule"`
	}
	decodeInto(t, resp, &out)
	return out.Rule
}

func TestRuleItemCheckKeyRoundTripsThroughTheAPI(t *testing.T) {
	f := newRulesFixture(t, "rules-item-key")
	host := f.host.String()

	resp := postJSON(t, f.operatorC, f.rulesURL(), itemDiskRuleBody(host, "disk-data"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	created := decodeRule(t, resp)
	if created.ItemCheckKey != "disk-data" || created.CheckKey != "disk" {
		t.Fatalf("created = %+v, want item_check_key disk-data and the plugin's check_key", created)
	}

	resp = requestJSON(t, f.viewerC, http.MethodGet, f.rulesURL()+"/"+created.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if got := decodeRule(t, resp); got.ItemCheckKey != "disk-data" {
		t.Fatalf("read back = %+v, want item_check_key disk-data", got)
	}

	// Update is a full replace: leaving the field out falls back to the
	// plugin's key, and the read shape omits it.
	resp = requestJSON(t, f.operatorC, http.MethodPut, f.rulesURL()+"/"+created.ID, itemDiskRuleBody(host, ""))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	body := mustReadBody(t, resp)
	if strings.Contains(body, "item_check_key") {
		t.Fatalf("update answer %s still names item_check_key, want it omitted once empty", body)
	}

	t.Run("refused on a non-item scope", func(t *testing.T) {
		b := map[string]any{
			"name": "host disk", "scope": "host", "host_id": host, "item_check_key": "disk-data",
			"metric": "disk.used_pct", "severity": "warn", "mode": "form",
			"form": map[string]any{"metric": "disk.used_pct", "op": ">", "value": 70},
		}
		resp := postJSON(t, f.operatorC, f.rulesURL(), b)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("create host rule with item_check_key = %d %s, want 422", resp.StatusCode, mustReadBody(t, resp))
		}
		resp = postJSON(t, f.operatorC, f.rulesURL()+"/validate", b)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("validate host rule with item_check_key = %d %s, want 422", resp.StatusCode, mustReadBody(t, resp))
		}
	})

	t.Run("refused when not a check key", func(t *testing.T) {
		resp := postJSON(t, f.operatorC, f.rulesURL(), itemDiskRuleBody(host, "Disk Data"))
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("create with item_check_key %q = %d %s, want 422", "Disk Data", resp.StatusCode, mustReadBody(t, resp))
		}
	})
}

// TestEffectiveRulesJudgeEveryKeyOfAPlugin: a second disk check on the
// host, under its own key, is judged by the organization's disk rules, and
// an item rule naming that key wins its tuple.
func TestEffectiveRulesJudgeEveryKeyOfAPlugin(t *testing.T) {
	f := newRulesFixture(t, "rules-any-key")
	host := f.host.String()

	resp := postJSON(t, f.operatorC, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/checks", map[string]any{
		"key": "disk-data", "plugin": "disk", "interval_seconds": 60, "binding_kind": "host", "host_id": host,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create disk-data check = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	if _, err := f.s.Checks.RecordObservations(scoped, f.orgID, []checks.Observation{
		{HostID: f.host, CheckKey: "disk-data", Item: "/data", Metric: "disk.used_pct", At: time.Now(), Value: floatPtr(42)},
	}); err != nil {
		t.Fatal(err)
	}
	orgRule := f.createRule(t, f.operatorC, map[string]any{
		"name": "org disk", "scope": "org", "metric": "disk.used_pct", "severity": "warn", "mode": "form",
		"form": map[string]any{"metric": "disk.used_pct", "op": ">", "value": 60},
	})

	effective := func(t *testing.T) []effectiveRuleBody {
		t.Helper()
		resp := requestJSON(t, f.viewerC, http.MethodGet, f.hostURL(f.host)+"/effective-rules", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("effective-rules = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		var out struct {
			Effective []effectiveRuleBody `json:"effective"`
		}
		decodeInto(t, resp, &out)
		return out.Effective
	}
	warnOn := func(list []effectiveRuleBody, checkKey string) *effectiveRuleBody {
		for i, e := range list {
			if e.CheckKey == checkKey && e.Item == "/data" && e.Metric == "disk.used_pct" && e.Severity == "warn" {
				return &list[i]
			}
		}
		return nil
	}

	e := warnOn(effective(t), "disk-data")
	if e == nil || !e.Observed || e.Winner.Scope != "org" || e.Winner.Metric != "disk.used_pct" {
		t.Fatalf("disk-data tuple = %+v, want it observed and judged by an org disk rule", e)
	}
	if e.Winner.ID != orgRule.ID && !hasRuleID(e.Shadowed, orgRule.ID) {
		t.Fatalf("disk-data tuple = %+v, want the new org rule among its candidates", e)
	}

	item := f.createRule(t, f.operatorC, itemDiskRuleBody(host, "disk-data"))
	e = warnOn(effective(t), "disk-data")
	if e == nil || e.Winner.ID != item.ID || !e.Observed {
		t.Fatalf("disk-data tuple = %+v, want the item rule naming disk-data to win it", e)
	}
	if plain := warnOn(effective(t), "disk"); plain != nil {
		t.Fatalf("an effective for disk:/data = %+v, want none: the item rule names disk-data and the host reports no /data under disk", plain)
	}
}
