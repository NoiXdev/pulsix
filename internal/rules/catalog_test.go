package rules_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/rules"
)

// TestCatalogueMatchesShippedPlugins pins the catalogue against the exact
// metrics v0.3b-1's four plugins emit (internal/agent/checks/plugins/{cpu,
// memory,disk,service}/), cross-checked against the brief: an entry missing
// here is a metric no rule could ever be written against; an entry that
// doesn't match the plugin's own reality is a silently wrong type check.
//
// cpu.load1/5/15 (internal/agent/checks/plugins/cpu/cpu.go, Collect) were
// missing from the plan's original eight-entry list — a
// controller-confirmed omission, not a deliberate cut (the architecture
// spec's own flagship rule example,
// avg(cpu.load5, 10m) > fact("cpu_count") * 0.8, is unwritable without
// cpu.load5 in the catalogue) — so the table below carries eleven entries.
//
// Since backlog 3.54 Catalogue() is derived from internal/checkplugins'
// descriptors; this literal table is the independent golden list it is
// pinned against, so a descriptor edit that drops, renames or re-kinds a
// metric fails here rather than silently unmaking every rule naming it.
func TestCatalogueMatchesShippedPlugins(t *testing.T) {
	want := map[string]rules.CatalogueEntry{
		"cpu.used_pct":         {CheckKey: "cpu", Kind: rules.MetricNumeric},
		"cpu.load1":            {CheckKey: "cpu", Kind: rules.MetricNumeric},
		"cpu.load5":            {CheckKey: "cpu", Kind: rules.MetricNumeric},
		"cpu.load15":           {CheckKey: "cpu", Kind: rules.MetricNumeric},
		"mem.used_pct":         {CheckKey: "memory", Kind: rules.MetricNumeric},
		"mem.available_bytes":  {CheckKey: "memory", Kind: rules.MetricNumeric},
		"swap.used_pct":        {CheckKey: "memory", Kind: rules.MetricNumeric},
		"disk.used_pct":        {CheckKey: "disk", Kind: rules.MetricNumeric},
		"disk.free_bytes":      {CheckKey: "disk", Kind: rules.MetricNumeric},
		"disk.inodes_used_pct": {CheckKey: "disk", Kind: rules.MetricNumeric},
		"service.state":        {CheckKey: "service", Kind: rules.MetricDiscrete},
		// Backlog 3.7: the network and uptime plugins
		// (internal/agent/checks/plugins/network/, uptime/).
		"net.rx_bytes_per_s":  {CheckKey: "network", Kind: rules.MetricNumeric},
		"net.tx_bytes_per_s":  {CheckKey: "network", Kind: rules.MetricNumeric},
		"net.rx_errors_per_s": {CheckKey: "network", Kind: rules.MetricNumeric},
		"net.tx_errors_per_s": {CheckKey: "network", Kind: rules.MetricNumeric},
		"net.rx_drops_per_s":  {CheckKey: "network", Kind: rules.MetricNumeric},
		"net.tx_drops_per_s":  {CheckKey: "network", Kind: rules.MetricNumeric},
		"uptime.seconds":      {CheckKey: "uptime", Kind: rules.MetricNumeric},
		// Backlog 3.13: the synthetic plugins the manager probes
		// (internal/probe).
		"icmp.up":               {CheckKey: "icmp", Kind: rules.MetricNumeric},
		"icmp.failure":          {CheckKey: "icmp", Kind: rules.MetricDiscrete},
		"icmp.loss_pct":         {CheckKey: "icmp", Kind: rules.MetricNumeric},
		"icmp.rtt_ms":           {CheckKey: "icmp", Kind: rules.MetricNumeric},
		"tcp.up":                {CheckKey: "tcp", Kind: rules.MetricNumeric},
		"tcp.failure":           {CheckKey: "tcp", Kind: rules.MetricDiscrete},
		"tcp.connect_ms":        {CheckKey: "tcp", Kind: rules.MetricNumeric},
		"tcp.banner_match":      {CheckKey: "tcp", Kind: rules.MetricNumeric},
		"http.up":               {CheckKey: "http", Kind: rules.MetricNumeric},
		"http.failure":          {CheckKey: "http", Kind: rules.MetricDiscrete},
		"http.status_code":      {CheckKey: "http", Kind: rules.MetricNumeric},
		"http.response_ms":      {CheckKey: "http", Kind: rules.MetricNumeric},
		"http.dns_ms":           {CheckKey: "http", Kind: rules.MetricNumeric},
		"http.connect_ms":       {CheckKey: "http", Kind: rules.MetricNumeric},
		"http.tls_ms":           {CheckKey: "http", Kind: rules.MetricNumeric},
		"http.ttfb_ms":          {CheckKey: "http", Kind: rules.MetricNumeric},
		"http.transfer_ms":      {CheckKey: "http", Kind: rules.MetricNumeric},
		"http.redirects":        {CheckKey: "http", Kind: rules.MetricNumeric},
		"http.content_bytes":    {CheckKey: "http", Kind: rules.MetricNumeric},
		"http.cert_expiry_days": {CheckKey: "http", Kind: rules.MetricNumeric},
		"http.cert_issuer":      {CheckKey: "http", Kind: rules.MetricDiscrete},
		"http.body_match":       {CheckKey: "http", Kind: rules.MetricNumeric},
		"http.json.value":       {CheckKey: "http", Kind: rules.MetricNumeric},
		"http.json.text":        {CheckKey: "http", Kind: rules.MetricDiscrete},
		"http.assertion":        {CheckKey: "http", Kind: rules.MetricDiscrete},
		"http.assertion_actual": {CheckKey: "http", Kind: rules.MetricDiscrete},
		"tls.up":                {CheckKey: "tls", Kind: rules.MetricNumeric},
		"tls.failure":           {CheckKey: "tls", Kind: rules.MetricDiscrete},
		"tls.handshake_ms":      {CheckKey: "tls", Kind: rules.MetricNumeric},
		"tls.cert_valid":        {CheckKey: "tls", Kind: rules.MetricNumeric},
		"tls.cert_expiry_days":  {CheckKey: "tls", Kind: rules.MetricNumeric},
		"tls.cert_issuer":       {CheckKey: "tls", Kind: rules.MetricDiscrete},
		"dns.up":                {CheckKey: "dns", Kind: rules.MetricNumeric},
		"dns.failure":           {CheckKey: "dns", Kind: rules.MetricDiscrete},
		"dns.resolve_ms":        {CheckKey: "dns", Kind: rules.MetricNumeric},
		"dns.answer_match":      {CheckKey: "dns", Kind: rules.MetricNumeric},
		// Backlog 3.12: the docker plugin (internal/agent/checks/plugins/docker/).
		"docker.state":                 {CheckKey: "docker", Kind: rules.MetricDiscrete},
		"docker.health":                {CheckKey: "docker", Kind: rules.MetricDiscrete},
		"docker.restart_count":         {CheckKey: "docker", Kind: rules.MetricNumeric},
		"docker.exit_code":             {CheckKey: "docker", Kind: rules.MetricNumeric},
		"docker.containers_running":    {CheckKey: "docker", Kind: rules.MetricNumeric},
		"docker.containers_total":      {CheckKey: "docker", Kind: rules.MetricNumeric},
		"docker.containers_unreported": {CheckKey: "docker", Kind: rules.MetricNumeric},
		// Backlog 3.12a: per-container usage, with the resources option.
		"docker.cpu_percent":             {CheckKey: "docker", Kind: rules.MetricNumeric},
		"docker.memory_bytes":            {CheckKey: "docker", Kind: rules.MetricNumeric},
		"docker.memory_percent":          {CheckKey: "docker", Kind: rules.MetricNumeric},
		"docker.net_rx_bytes_per_s":      {CheckKey: "docker", Kind: rules.MetricNumeric},
		"docker.net_tx_bytes_per_s":      {CheckKey: "docker", Kind: rules.MetricNumeric},
		"docker.block_read_bytes_per_s":  {CheckKey: "docker", Kind: rules.MetricNumeric},
		"docker.block_write_bytes_per_s": {CheckKey: "docker", Kind: rules.MetricNumeric},
		"docker.containers_unsampled":    {CheckKey: "docker", Kind: rules.MetricNumeric},

		// Backlog 3.9: the hardware plugins (temperature, smart, raid).
		"temp.celsius":                   {CheckKey: "temperature", Kind: rules.MetricNumeric},
		"temp.high_celsius":              {CheckKey: "temperature", Kind: rules.MetricNumeric},
		"temp.critical_celsius":          {CheckKey: "temperature", Kind: rules.MetricNumeric},
		"temp.sensors":                   {CheckKey: "temperature", Kind: rules.MetricNumeric},
		"smart.devices":                  {CheckKey: "smart", Kind: rules.MetricNumeric},
		"smart.passed":                   {CheckKey: "smart", Kind: rules.MetricNumeric},
		"smart.temperature_celsius":      {CheckKey: "smart", Kind: rules.MetricNumeric},
		"smart.power_on_hours":           {CheckKey: "smart", Kind: rules.MetricNumeric},
		"smart.reallocated_sectors":      {CheckKey: "smart", Kind: rules.MetricNumeric},
		"smart.pending_sectors":          {CheckKey: "smart", Kind: rules.MetricNumeric},
		"smart.offline_uncorrectable":    {CheckKey: "smart", Kind: rules.MetricNumeric},
		"smart.nvme_percentage_used":     {CheckKey: "smart", Kind: rules.MetricNumeric},
		"smart.nvme_available_spare_pct": {CheckKey: "smart", Kind: rules.MetricNumeric},
		"smart.nvme_media_errors":        {CheckKey: "smart", Kind: rules.MetricNumeric},
		"smart.power_mode":               {CheckKey: "smart", Kind: rules.MetricDiscrete}, // backlog 3.9b
		"raid.state":                     {CheckKey: "raid", Kind: rules.MetricDiscrete},
		"raid.devices_total":             {CheckKey: "raid", Kind: rules.MetricNumeric},
		"raid.devices_active":            {CheckKey: "raid", Kind: rules.MetricNumeric},
		"raid.devices_failed":            {CheckKey: "raid", Kind: rules.MetricNumeric},
		"raid.sync_pct":                  {CheckKey: "raid", Kind: rules.MetricNumeric},
		"raid.arrays":                    {CheckKey: "raid", Kind: rules.MetricNumeric},

		// Backlog 3.9a: the zfs plugin (internal/agent/checks/plugins/zfs/).
		"zfs.state":             {CheckKey: "zfs", Kind: rules.MetricDiscrete},
		"zfs.capacity_pct":      {CheckKey: "zfs", Kind: rules.MetricNumeric},
		"zfs.size_bytes":        {CheckKey: "zfs", Kind: rules.MetricNumeric},
		"zfs.free_bytes":        {CheckKey: "zfs", Kind: rules.MetricNumeric},
		"zfs.fragmentation_pct": {CheckKey: "zfs", Kind: rules.MetricNumeric},
		"zfs.errors_read":       {CheckKey: "zfs", Kind: rules.MetricNumeric},
		"zfs.errors_write":      {CheckKey: "zfs", Kind: rules.MetricNumeric},
		"zfs.errors_checksum":   {CheckKey: "zfs", Kind: rules.MetricNumeric},
		"zfs.data_errors":       {CheckKey: "zfs", Kind: rules.MetricNumeric},
		"zfs.devices_unhealthy": {CheckKey: "zfs", Kind: rules.MetricNumeric},
		"zfs.scan":              {CheckKey: "zfs", Kind: rules.MetricDiscrete},
		"zfs.scan_pct":          {CheckKey: "zfs", Kind: rules.MetricNumeric},
		"zfs.scrub_age_days":    {CheckKey: "zfs", Kind: rules.MetricNumeric},
		"zfs.pools":             {CheckKey: "zfs", Kind: rules.MetricNumeric},

		// Backlog 3.10: the database plugins (postgres, mysql, redis).
		"pg.up":                      {CheckKey: "postgres", Kind: rules.MetricNumeric},
		"pg.connections":             {CheckKey: "postgres", Kind: rules.MetricNumeric},
		"pg.max_connections":         {CheckKey: "postgres", Kind: rules.MetricNumeric},
		"pg.connections_pct":         {CheckKey: "postgres", Kind: rules.MetricNumeric},
		"pg.in_recovery":             {CheckKey: "postgres", Kind: rules.MetricNumeric},
		"pg.replication_lag_seconds": {CheckKey: "postgres", Kind: rules.MetricNumeric},
		"pg.replication_lag_bytes":   {CheckKey: "postgres", Kind: rules.MetricNumeric},
		"pg.oldest_xact_seconds":     {CheckKey: "postgres", Kind: rules.MetricNumeric},
		"pg.database_size_bytes":     {CheckKey: "postgres", Kind: rules.MetricNumeric},
		"pg.databases_unreported":    {CheckKey: "postgres", Kind: rules.MetricNumeric},
		"pg.replicas":                {CheckKey: "postgres", Kind: rules.MetricNumeric},
		"pg.replica_lag_bytes":       {CheckKey: "postgres", Kind: rules.MetricNumeric},
		"pg.replica_lag_seconds":     {CheckKey: "postgres", Kind: rules.MetricNumeric},
		"pg.replica_state":           {CheckKey: "postgres", Kind: rules.MetricDiscrete},

		"mysql.up":                      {CheckKey: "mysql", Kind: rules.MetricNumeric},
		"mysql.connections":             {CheckKey: "mysql", Kind: rules.MetricNumeric},
		"mysql.max_connections":         {CheckKey: "mysql", Kind: rules.MetricNumeric},
		"mysql.connections_pct":         {CheckKey: "mysql", Kind: rules.MetricNumeric},
		"mysql.replication_state":       {CheckKey: "mysql", Kind: rules.MetricDiscrete},
		"mysql.replication_lag_seconds": {CheckKey: "mysql", Kind: rules.MetricNumeric},
		"mysql.slow_queries_per_s":      {CheckKey: "mysql", Kind: rules.MetricNumeric},
		"mysql.schema_size_bytes":       {CheckKey: "mysql", Kind: rules.MetricNumeric},
		"mysql.schemas_unreported":      {CheckKey: "mysql", Kind: rules.MetricNumeric},

		"redis.up":                          {CheckKey: "redis", Kind: rules.MetricNumeric},
		"redis.memory_used_bytes":           {CheckKey: "redis", Kind: rules.MetricNumeric},
		"redis.memory_max_bytes":            {CheckKey: "redis", Kind: rules.MetricNumeric},
		"redis.memory_pct":                  {CheckKey: "redis", Kind: rules.MetricNumeric},
		"redis.clients":                     {CheckKey: "redis", Kind: rules.MetricNumeric},
		"redis.role":                        {CheckKey: "redis", Kind: rules.MetricDiscrete},
		"redis.replication_link":            {CheckKey: "redis", Kind: rules.MetricDiscrete},
		"redis.replication_lag_seconds":     {CheckKey: "redis", Kind: rules.MetricNumeric},
		"redis.evicted_keys_per_s":          {CheckKey: "redis", Kind: rules.MetricNumeric},
		"redis.rdb_last_bgsave_status":      {CheckKey: "redis", Kind: rules.MetricDiscrete},
		"redis.rdb_last_save_age_seconds":   {CheckKey: "redis", Kind: rules.MetricNumeric},
		"redis.rdb_changes_since_last_save": {CheckKey: "redis", Kind: rules.MetricNumeric},
		"redis.aof_enabled":                 {CheckKey: "redis", Kind: rules.MetricNumeric},
		"redis.aof_last_write_status":       {CheckKey: "redis", Kind: rules.MetricDiscrete},
		"redis.aof_last_bgrewrite_status":   {CheckKey: "redis", Kind: rules.MetricDiscrete},
		"redis.loading":                     {CheckKey: "redis", Kind: rules.MetricNumeric},

		// Backlog 3.11: the web server plugins.
		"nginx.up":                          {CheckKey: "nginx", Kind: rules.MetricNumeric},
		"nginx.connections_active":          {CheckKey: "nginx", Kind: rules.MetricNumeric},
		"nginx.connections_reading":         {CheckKey: "nginx", Kind: rules.MetricNumeric},
		"nginx.connections_writing":         {CheckKey: "nginx", Kind: rules.MetricNumeric},
		"nginx.connections_waiting":         {CheckKey: "nginx", Kind: rules.MetricNumeric},
		"nginx.requests_per_s":              {CheckKey: "nginx", Kind: rules.MetricNumeric},
		"nginx.connections_accepted_per_s":  {CheckKey: "nginx", Kind: rules.MetricNumeric},
		"nginx.connections_dropped_per_s":   {CheckKey: "nginx", Kind: rules.MetricNumeric},
		"apache.up":                         {CheckKey: "apache", Kind: rules.MetricNumeric},
		"apache.workers_busy":               {CheckKey: "apache", Kind: rules.MetricNumeric},
		"apache.workers_idle":               {CheckKey: "apache", Kind: rules.MetricNumeric},
		"apache.workers_busy_pct":           {CheckKey: "apache", Kind: rules.MetricNumeric},
		"apache.requests_per_s":             {CheckKey: "apache", Kind: rules.MetricNumeric},
		"apache.bytes_per_s":                {CheckKey: "apache", Kind: rules.MetricNumeric},
		"apache.workers_max":                {CheckKey: "apache", Kind: rules.MetricNumeric},
		"apache.workers_busy_max_pct":       {CheckKey: "apache", Kind: rules.MetricNumeric},
		"caddy.up":                          {CheckKey: "caddy", Kind: rules.MetricNumeric},
		"caddy.requests_in_flight":          {CheckKey: "caddy", Kind: rules.MetricNumeric},
		"caddy.requests_per_s":              {CheckKey: "caddy", Kind: rules.MetricNumeric},
		"caddy.errors_5xx_per_s":            {CheckKey: "caddy", Kind: rules.MetricNumeric},
		"phpfpm.pools_down":                 {CheckKey: "phpfpm", Kind: rules.MetricNumeric},
		"phpfpm.up":                         {CheckKey: "phpfpm", Kind: rules.MetricNumeric},
		"phpfpm.processes_active":           {CheckKey: "phpfpm", Kind: rules.MetricNumeric},
		"phpfpm.processes_idle":             {CheckKey: "phpfpm", Kind: rules.MetricNumeric},
		"phpfpm.processes_total":            {CheckKey: "phpfpm", Kind: rules.MetricNumeric},
		"phpfpm.processes_busy_pct":         {CheckKey: "phpfpm", Kind: rules.MetricNumeric},
		"phpfpm.listen_queue":               {CheckKey: "phpfpm", Kind: rules.MetricNumeric},
		"phpfpm.requests_per_s":             {CheckKey: "phpfpm", Kind: rules.MetricNumeric},
		"phpfpm.max_children_reached_per_s": {CheckKey: "phpfpm", Kind: rules.MetricNumeric},
		"phpfpm.slow_requests_per_s":        {CheckKey: "phpfpm", Kind: rules.MetricNumeric},
		"script.status":                     {CheckKey: "script", Kind: rules.MetricDiscrete},
		"script.output":                     {CheckKey: "script", Kind: rules.MetricDiscrete},
		"script.exit_code":                  {CheckKey: "script", Kind: rules.MetricNumeric},
		"script.duration_seconds":           {CheckKey: "script", Kind: rules.MetricNumeric},
		"script.value":                      {CheckKey: "script", Kind: rules.MetricNumeric},
		"snmp.up":                           {CheckKey: "snmp", Kind: rules.MetricNumeric},
		"snmp.failure":                      {CheckKey: "snmp", Kind: rules.MetricDiscrete},
		"snmp.response_ms":                  {CheckKey: "snmp", Kind: rules.MetricNumeric},
		"snmp.uptime_seconds":               {CheckKey: "snmp", Kind: rules.MetricNumeric},
		"snmp.value":                        {CheckKey: "snmp", Kind: rules.MetricNumeric},
		"snmp.text":                         {CheckKey: "snmp", Kind: rules.MetricDiscrete},
		"snmp.if.status":                    {CheckKey: "snmp", Kind: rules.MetricDiscrete},
		"snmp.if.in_bps":                    {CheckKey: "snmp", Kind: rules.MetricNumeric},
		"snmp.if.out_bps":                   {CheckKey: "snmp", Kind: rules.MetricNumeric},
		"snmp.if.errors":                    {CheckKey: "snmp", Kind: rules.MetricNumeric},
	}
	got := rules.Catalogue()
	if len(got) != len(want) {
		t.Fatalf("Catalogue() has %d entries, want %d: got=%+v", len(got), len(want), got)
	}
	for name, wantEntry := range want {
		gotEntry, ok := got[name]
		if !ok {
			t.Fatalf("Catalogue() is missing %q", name)
		}
		if gotEntry != wantEntry {
			t.Fatalf("Catalogue()[%q] = %+v, want %+v", name, gotEntry, wantEntry)
		}
	}
}

// TestCatalogueCheckKeyMatchesEmittingPlugin is a second, independent
// check on top of TestCatalogueMatchesShippedPlugins's literal table: a
// metric's own namespace (the part before its first '.') names the plugin
// that reports it — cpu.*, mem.*/swap.* (both properties of the memory
// plugin's two gopsutil calls), disk.*, service.* — and CatalogueEntry's
// CheckKey must always agree with that namespace. A hand-typed CheckKey
// that silently disagreed with its own metric's prefix (a copy-paste slip
// from the row above, say) would pass a plain struct-equality check
// against an equally wrong "want" table but fails this one, since it is
// derived from the metric name rather than restated by hand a second time.
func TestCatalogueCheckKeyMatchesEmittingPlugin(t *testing.T) {
	prefixCheckKey := map[string]string{
		"cpu":     "cpu",
		"mem":     "memory",
		"swap":    "memory",
		"disk":    "disk",
		"service": "service",
		"net":     "network",
		"uptime":  "uptime",
		"icmp":    "icmp",
		"tcp":     "tcp",
		"http":    "http",
		"tls":     "tls",
		"dns":     "dns",
		"docker":  "docker",
		"temp":    "temperature",
		"smart":   "smart",
		"raid":    "raid",
		"zfs":     "zfs",
		"pg":      "postgres",
		"mysql":   "mysql",
		"redis":   "redis",
		"nginx":   "nginx",
		"apache":  "apache",
		"caddy":   "caddy",
		"phpfpm":  "phpfpm",
		"script":  "script",
		"snmp":    "snmp",
	}
	for name, entry := range rules.Catalogue() {
		prefix, _, ok := strings.Cut(name, ".")
		if !ok {
			t.Fatalf("metric %q has no '.'-separated namespace to check", name)
		}
		wantKey, known := prefixCheckKey[prefix]
		if !known {
			t.Fatalf("metric %q has an unrecognized namespace %q — add it to prefixCheckKey", name, prefix)
		}
		if entry.CheckKey != wantKey {
			t.Fatalf("Catalogue()[%q].CheckKey = %q, want %q (the %q namespace's own plugin)", name, entry.CheckKey, wantKey, prefix)
		}
	}
}

// TestCatalogueIsFreshEveryCall guards the "no package-level mutable
// state" constraint: the returned map must be safe for a caller to mutate
// without corrupting a later call's result.
func TestCatalogueIsFreshEveryCall(t *testing.T) {
	a := rules.Catalogue()
	a["made.up.metric"] = rules.CatalogueEntry{CheckKey: "x", Kind: rules.MetricNumeric}
	b := rules.Catalogue()
	if _, ok := b["made.up.metric"]; ok {
		t.Fatal("mutating one Catalogue() result leaked into a later call — the catalogue must not be shared mutable state")
	}
}

func TestMinMaxWindowBounds(t *testing.T) {
	if rules.MinWindow != time.Minute {
		t.Fatalf("MinWindow = %s, want %s", rules.MinWindow, time.Minute)
	}
	if rules.MaxWindow != 24*time.Hour {
		t.Fatalf("MaxWindow = %s, want %s", rules.MaxWindow, 24*time.Hour)
	}
}

// TestTypeCheckValid confirms well-formed trees across every node kind are
// accepted — the negative tests below would be meaningless if TypeCheck
// rejected everything.
func TestTypeCheckValid(t *testing.T) {
	tests := []struct {
		name string
		node rules.Node
	}{
		{"compare of two numbers", compare(">", number(90), number(0))},
		{"compare of a metric", compare(">", metric("cpu.used_pct"), number(90))},
		{"compare of a window within bounds", compare(">", window("cpu.used_pct", "avg", 600), number(90))},
		{"compare of a fact", compare(">", fact("cpu_count"), number(0))},
		{"arith nested inside compare", compare("==", arith("+", number(1), arith("*", number(2), number(3))), number(7))},
		{"label == on a discrete metric", label("service.state", "==", "active")},
		{"label in on a discrete metric", label("service.state", "in", "active", "activating")},
		{"and of two booleans", and(compare(">", number(1), number(0)), label("service.state", "==", "active"))},
		{"or of two booleans", or(compare(">", number(1), number(0)), label("service.state", "==", "active"))},
		{"not of a boolean", not(compare(">", number(1), number(0)))},
		{"window at exactly MinWindow", compare(">", window("cpu.used_pct", "avg", 60), number(0))},
		{"window at exactly MaxWindow", compare(">", window("cpu.used_pct", "avg", 86400), number(0))},
		{"compare of a network rate", compare(">", metric("net.rx_errors_per_s"), number(0))},
		{"window over a network rate", compare(">", window("net.tx_bytes_per_s", "avg", 600), number(1e6))},
		{"compare of uptime", compare("<", metric("uptime.seconds"), number(600))},
		{"compare of a synthetic up", compare("==", metric("http.up"), number(0))},
		{"label == on a container state", label("docker.state", "==", "exited")},
		{"compare of a container's restart count", compare(">", metric("docker.restart_count"), number(3))},
		{"compare of a certificate's days left", compare("<", metric("tls.cert_expiry_days"), number(14))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := rules.TypeCheck(tc.node); err != nil {
				t.Fatalf("TypeCheck(%+v) = %v, want nil", tc.node, err)
			}
		})
	}
}

// TestTypeCheckRejects is the brief's TypeCheck table: unknown metric,
// numeric operator on a discrete metric, window below MinWindow, window
// above MaxWindow, and a non-boolean root — each asserting the reported
// Pos, so the parser (a later task) can point straight at the offending
// source position.
func TestTypeCheckRejects(t *testing.T) {
	tests := []struct {
		name    string
		node    rules.Node
		wantPos int
	}{
		{
			"unknown metric",
			compare(">", rules.Node{Kind: rules.KindMetric, Name: "no.such.metric", Pos: 5}, number(0)),
			5,
		},
		{
			"unknown metric in a window",
			compare(">", rules.Node{Kind: rules.KindWindow, Name: "no.such.metric", Agg: "avg", Seconds: 600, Pos: 7}, number(0)),
			7,
		},
		{
			"unknown metric in a label",
			rules.Node{Kind: rules.KindLabel, Name: "no.such.metric", Op: "==", Str: "x", Pos: 9},
			9,
		},
		{
			"numeric operator on a discrete metric",
			compare(">", rules.Node{Kind: rules.KindMetric, Name: "service.state", Pos: 11}, number(0)),
			11,
		},
		{
			"label on a numeric metric",
			rules.Node{Kind: rules.KindLabel, Name: "cpu.used_pct", Op: "==", Str: "x", Pos: 13},
			13,
		},
		{
			"window below MinWindow",
			compare(">", rules.Node{Kind: rules.KindWindow, Name: "cpu.used_pct", Agg: "avg", Seconds: 30, Pos: 17}, number(0)),
			17,
		},
		{
			"window above MaxWindow",
			compare(">", rules.Node{Kind: rules.KindWindow, Name: "cpu.used_pct", Agg: "avg", Seconds: 86401, Pos: 19}, number(0)),
			19,
		},
		{
			"non-boolean root",
			rules.Node{Kind: rules.KindNumber, Num: 1, Pos: 23},
			23,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := rules.TypeCheck(tc.node)
			if err == nil {
				t.Fatalf("TypeCheck(%+v) = nil, want an error", tc.node)
			}
			if !errors.Is(err, rules.ErrInvalidRule) {
				t.Fatalf("TypeCheck error %v does not wrap ErrInvalidRule", err)
			}
			var tcErr *rules.TypeCheckError
			if !errors.As(err, &tcErr) {
				t.Fatalf("TypeCheck error %v is not a *rules.TypeCheckError", err)
			}
			if tcErr.Pos != tc.wantPos {
				t.Fatalf("TypeCheck error Pos = %d, want %d (err=%v)", tcErr.Pos, tc.wantPos, err)
			}
		})
	}
}

// TestTypeCheckErrorMessageCarriesPosAndMsg pins TypeCheckError.Error's
// rendering, since nothing above calls it directly (they only inspect the
// struct's fields).
func TestTypeCheckErrorMessageCarriesPosAndMsg(t *testing.T) {
	err := &rules.TypeCheckError{Pos: 42, Msg: "something went wrong"}
	got := err.Error()
	if !strings.Contains(got, "42") || !strings.Contains(got, "something went wrong") {
		t.Fatalf("Error() = %q, want it to mention the position 42 and the message", got)
	}
}

// TestTypeCheckRejectsMalformedNodes exercises the defensive branches
// TypeCheck carries for shapes a correct parser (Task 2) never produces
// but a hand-built or corrupted tree could: unknown operators and wrong
// argument counts at every operator node, and an empty and/or (which
// would otherwise report "the rule is invalid" for the wrong reason). All
// of these are ErrInvalidRule, same as the semantic failures above.
func TestTypeCheckRejectsMalformedNodes(t *testing.T) {
	tests := []struct {
		name string
		node rules.Node
	}{
		{"compare unknown operator", compare("~", number(1), number(1))},
		{"compare wrong arg count", rules.Node{Kind: rules.KindCompare, Op: ">", Args: []rules.Node{number(1)}}},
		{"label unknown operator", rules.Node{Kind: rules.KindLabel, Name: "service.state", Op: "~"}},
		{"and with no operands", rules.Node{Kind: rules.KindAnd}},
		{"or with no operands", rules.Node{Kind: rules.KindOr}},
		{"not wrong arg count", rules.Node{Kind: rules.KindNot, Args: []rules.Node{number(1), number(2)}}},
		{"arith unknown operator", compare("==", arith("%", number(1), number(1)), number(0))},
		{"arith wrong arg count", compare("==", rules.Node{Kind: rules.KindArith, Op: "+", Args: []rules.Node{number(1)}}, number(0))},
		{"fact with no name", compare(">", fact(""), number(0))},
		{"window unknown aggregate", compare(">", rules.Node{Kind: rules.KindWindow, Name: "cpu.used_pct", Agg: "median", Seconds: 600}, number(0))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := rules.TypeCheck(tc.node); err == nil || !errors.Is(err, rules.ErrInvalidRule) {
				t.Fatalf("TypeCheck(%+v) = %v, want an ErrInvalidRule", tc.node, err)
			}
		})
	}
}
