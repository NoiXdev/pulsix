package caddy

import (
	"os"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseMetrics(t *testing.T) {
	tests := []struct {
		name string
		body string
		want metrics
	}{
		{"plain and labelled", "go_goroutines 1\ncaddy_http_requests_in_flight 3\ncaddy_http_requests_total{server=\"a\",handler=\"h\"} 7\n",
			metrics{inflight: 3, haveInflight: true, requests: 7, haveRequests: true}},
		{"timestamp after the value", "caddy_http_requests_total{server=\"a\",handler=\"h\"} 7 1731491234000\n",
			metrics{requests: 7, haveRequests: true}},
		{"tabs and spaces", "caddy_http_requests_total {server=\"a\" , handler=\"h\" ,}\t 7  \n",
			metrics{requests: 7, haveRequests: true}},
		{"float counter", "caddy_http_requests_total{server=\"a\",handler=\"h\"} 1.5e3\n",
			metrics{requests: 1500, haveRequests: true}},
		{"escapes in label values", "caddy_http_requests_total{server=\"a\\\\b\\\"c\\nd\",handler=\"h}x\"} 7\ncaddy_http_requests_total{server=\"a\\\\b\\\"c\\nd\",handler=\"h2\"} 9\n",
			metrics{requests: 9, haveRequests: true}},
		{"crlf", "caddy_http_requests_total{server=\"a\",handler=\"h\"} 7\r\ngo_goroutines 1\r\n",
			metrics{requests: 7, haveRequests: true}},
		{"comments and blank lines", "# HELP caddy_http_requests_total x\n\n   \n# TYPE caddy_http_requests_total counter\ncaddy_http_requests_total 4\n",
			metrics{requests: 4, haveRequests: true}},
		{"no trailing newline", "caddy_http_requests_total 4", metrics{requests: 4, haveRequests: true}},
		{"missing handler and server", "caddy_http_requests_total 4\ncaddy_http_requests_total{code=\"200\"} 5\n",
			metrics{requests: 9, haveRequests: true}},
		{"other families are skipped", "caddy_http_response_size_bytes_count{server=\"a\"} NaN\nfoo{bad=\"x\"} 1\ngo_goroutines 1\n",
			metrics{}},
		{"5xx codes", "caddy_http_request_duration_seconds_count{code=\"200\",handler=\"h\",server=\"a\"} 100\ncaddy_http_request_duration_seconds_count{code=\"500\",handler=\"h\",server=\"a\"} 3\ncaddy_http_request_duration_seconds_count{code=\"503\",handler=\"h\",server=\"a\"} 2\n",
			metrics{errs5xx: 5, have5xx: true}},
		{"NaN Inf and negative ignore the series", "caddy_http_requests_total{server=\"a\"} NaN\ncaddy_http_requests_total{server=\"b\"} +Inf\ncaddy_http_requests_total{server=\"c\"} -Inf\ncaddy_http_requests_total{server=\"d\"} -1\ncaddy_http_requests_total{server=\"e\"} 5\ncaddy_http_requests_in_flight{server=\"a\"} -2\n",
			metrics{requests: 5, haveRequests: true}},
		{"huge in-flight gauges stay finite", "caddy_http_requests_in_flight{server=\"a\"} 1e308\ncaddy_http_requests_in_flight{server=\"b\"} 1e308\ncaddy_http_requests_in_flight{server=\"c\"} 3\n",
			metrics{inflight: 3, haveInflight: true}},
		{"in-flight above 2^53 is ignored", "caddy_http_requests_in_flight{server=\"a\"} 1.8e16\ncaddy_http_requests_in_flight{server=\"b\"} 4\n",
			metrics{inflight: 4, haveInflight: true}},
		{"in-flight sums saturate", "caddy_http_requests_in_flight{server=\"a\"} 9007199254740992\ncaddy_http_requests_in_flight{server=\"b\"} 9007199254740992\n",
			metrics{inflight: 1 << 53, haveInflight: true}},
		{"long irrelevant lines are fine", "# HELP x " + strings.Repeat("y", 100<<10) + "\ngo_x{a=\"" + strings.Repeat("z", 100<<10) + "\"} 1\ncaddy_http_requests_total 2\n",
			metrics{requests: 2, haveRequests: true}},
		{"lookalike families are not counted", "caddy_http_requests_total_x 5\ncaddy_http_requests_in_flight_x 5\ncaddy_http_request_duration_seconds_count_x{code=\"500\"} 5\ncaddy_http_requests_total 1\n",
			metrics{requests: 1, haveRequests: true}},
		{"sum and bucket lines are not 5xx", "caddy_http_request_duration_seconds_count{code=\"200\",server=\"a\"} 5\ncaddy_http_request_duration_seconds_sum{code=\"500\",server=\"a\"} 9\ncaddy_http_request_duration_seconds_bucket{code=\"500\",server=\"a\",le=\"1\"} 9\n",
			metrics{have5xx: true}},
		{"above 2^53 is ignored", "caddy_http_requests_total{server=\"a\"} 9007199254740993\ncaddy_http_requests_total{server=\"b\"} 1e300\ncaddy_http_requests_total{server=\"c\"} 9007199254740992\n",
			metrics{requests: 1 << 53, haveRequests: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMetrics([]byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("parseMetrics = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseMetricsFormatErrors(t *testing.T) {
	long := strings.Repeat("x", 64<<10)
	tests := []struct{ name, body string }{
		{"empty", ""},
		{"only comments", "# HELP a b\n# TYPE a counter\n"},
		{"html", "<html><body>hi</body></html>\n"},
		{"json", "{\"a\": 1}\n"},
		{"over-long relevant line", "go_goroutines 1\ncaddy_http_requests_total{server=\"" + long + "\"} 1\n"},
		{"over-long relevant line without labels", "caddy_http_requests_total " + long + "\n"},
		{"plain text", "Welcome to Caddy\n"},
		{"plain text sentence", "Caddy is running fine 5\n"},
		{"other: no value", "go_goroutines\n"},
		{"other: bad value", "go_goroutines abc\n"},
		{"other: bad timestamp", "go_goroutines 1 x\n"},
		{"other: extra field", "go_goroutines 1 2 3\n"},
		{"other: unterminated labels", "go_goroutines{a=\"b\" 1\n"},
		{"other: unterminated value", "go_goroutines{a=\"b} 1\n"},
		{"relevant: bad label name", "caddy_http_requests_total{a} b=\"c\"} 5\n"},
		{"relevant: label name with a dash", "caddy_http_requests_total{a-b=\"c\"} 5\n"},
		{"relevant: digit-leading label name", "caddy_http_requests_total{1a=\"c\"} 5\n"},
		{"relevant: no value", "caddy_http_requests_total{server=\"a\"}\n"},
		{"relevant: bad value", "caddy_http_requests_total{server=\"a\"} abc\n"},
		{"relevant: bad timestamp", "caddy_http_requests_total{server=\"a\"} 1 yesterday\n"},
		{"relevant: extra field", "caddy_http_requests_total{server=\"a\"} 1 2 3\n"},
		{"relevant: unterminated labels", "caddy_http_requests_total{server=\"a\" 1\n"},
		{"relevant: unterminated value", "caddy_http_requests_total{server=\"a} 1\n"},
		{"relevant: label without equals", "caddy_http_requests_total{server} 1\n"},
		{"relevant: unquoted label", "caddy_http_requests_total{server=a} 1\n"},
		{"relevant: bad escape", "caddy_http_requests_total{server=\"a\\x\"} 1\n"},
		{"relevant: name run into value", "caddy_http_requests_total1\n"},
		{"other family with a bad name", "go_goroutines 1\n9bad 1\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseMetrics([]byte(tt.body)); err != webcheck.ErrFormat {
				t.Fatalf("parseMetrics err = %v, want %v", err, webcheck.ErrFormat)
			}
		})
	}
}

func TestParseRealFixture(t *testing.T) {
	got, err := parseMetrics([]byte(fixture(t, "caddy-2.8.txt")))
	if err != nil {
		t.Fatal(err)
	}
	// srv1 is the metrics site (1 request, 1 in flight), srv0 the respond site.
	want := metrics{inflight: 1, haveInflight: true, requests: 3, haveRequests: true, have5xx: true}
	if got != want {
		t.Fatalf("parseMetrics = %+v, want %+v", got, want)
	}
}

func TestParseTwoServersThreeHandlers(t *testing.T) {
	got, err := parseMetrics([]byte(fixture(t, "two-servers.txt")))
	if err != nil {
		t.Fatal(err)
	}
	// Per server the largest handler, then the servers: in flight 4+2,
	// requests 1000+200; 5xx per (server, handler) summed over 500-599
	// (srv0: 15, 4, 1; srv1: 7, code 600 excluded) then the largest: 15+7.
	want := metrics{inflight: 6, haveInflight: true, requests: 1200, haveRequests: true, errs5xx: 22, have5xx: true}
	if got != want {
		t.Fatalf("parseMetrics = %+v, want %+v", got, want)
	}
}

// A hostile page of many megabytes of other series is skipped without
// building anything.
func TestParseBigPageOfOtherSeries(t *testing.T) {
	body := strings.Repeat("go_memstats_x{a=\"b\"} 1\n", 150_000) + "caddy_http_requests_total 3\n"
	got, err := parseMetrics([]byte(body))
	if err != nil || got != (metrics{requests: 3, haveRequests: true}) {
		t.Fatalf("parseMetrics = %+v, %v", got, err)
	}
}
