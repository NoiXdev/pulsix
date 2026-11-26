package redis

import (
	"testing"
)

func TestPersistence(t *testing.T) {
	s := serve(t, nil, bulk(primaryInfo()))
	p, _ := newTestPlugin()
	obs := collect(t, p, fieldsFor(s, nil))
	mustLabel(t, obs, "redis.rdb_last_bgsave_status", "ok")
	mustValue(t, obs, "redis.rdb_last_save_age_seconds", 60)
	mustValue(t, obs, "redis.rdb_changes_since_last_save", 12)
	mustValue(t, obs, "redis.aof_enabled", 0)
	mustLabel(t, obs, "redis.aof_last_write_status", "ok")
	mustLabel(t, obs, "redis.aof_last_bgrewrite_status", "ok")
	mustValue(t, obs, "redis.loading", 0)
}

func TestPersistenceFailures(t *testing.T) {
	s := serve(t, nil, bulk(primaryInfo(
		"rdb_last_bgsave_status", "err", "aof_enabled", "1", "aof_last_write_status", "err",
		"aof_last_bgrewrite_status", "err", "loading", "1")))
	p, _ := newTestPlugin()
	obs := collect(t, p, fieldsFor(s, nil))
	mustLabel(t, obs, "redis.rdb_last_bgsave_status", "err")
	mustValue(t, obs, "redis.aof_enabled", 1)
	mustLabel(t, obs, "redis.aof_last_write_status", "err")
	mustLabel(t, obs, "redis.aof_last_bgrewrite_status", "err")
	mustValue(t, obs, "redis.loading", 1)
}

// The server's own time (Redis 6.2+) measures the age, so a clock skew
// between the agent and a remote server does not.
func TestSaveAgeUsesServerTime(t *testing.T) {
	s := serve(t, nil, bulk(primaryInfo("server_time_usec", "1000500123456")))
	p, _ := newTestPlugin()
	mustValue(t, collect(t, p, fieldsFor(s, nil)), "redis.rdb_last_save_age_seconds", 560)
}

func TestSaveAgeNeverNegative(t *testing.T) {
	s := serve(t, nil, bulk(primaryInfo("rdb_last_save_time", "1000100")))
	p, _ := newTestPlugin()
	mustValue(t, collect(t, p, fieldsFor(s, nil)), "redis.rdb_last_save_age_seconds", 0)
}

// A server that speaks the protocol without the persistence fields (or
// with values this plugin cannot read) reports errors, never zeros or ok.
func TestPersistenceFieldsMissing(t *testing.T) {
	tests := []struct {
		field, value, metric, err string
	}{
		{"rdb_last_bgsave_status", "-", "redis.rdb_last_bgsave_status", "redis: rdb_last_bgsave_status is not reported"},
		{"rdb_last_bgsave_status", "maybe", "redis.rdb_last_bgsave_status", "redis: rdb_last_bgsave_status is not ok or err"},
		{"rdb_last_save_time", "-", "redis.rdb_last_save_age_seconds", "redis: rdb_last_save_time is not reported"},
		{"rdb_last_save_time", "soon", "redis.rdb_last_save_age_seconds", "redis: rdb_last_save_time is not reported"},
		{"rdb_changes_since_last_save", "-", "redis.rdb_changes_since_last_save", "redis: rdb_changes_since_last_save is not reported"},
		{"aof_enabled", "-", "redis.aof_enabled", "redis: aof_enabled is not reported"},
		{"aof_enabled", "2", "redis.aof_enabled", "redis: aof_enabled is not 0 or 1"},
		{"aof_last_write_status", "-", "redis.aof_last_write_status", "redis: aof_last_write_status is not reported"},
		{"aof_last_bgrewrite_status", "-", "redis.aof_last_bgrewrite_status", "redis: aof_last_bgrewrite_status is not reported"},
		{"loading", "-", "redis.loading", "redis: loading is not reported"},
		{"loading", "yes", "redis.loading", "redis: loading is not 0 or 1"},
	}
	for _, tt := range tests {
		t.Run(tt.field+"="+tt.value, func(t *testing.T) {
			s := serve(t, nil, bulk(primaryInfo(tt.field, tt.value)))
			p, _ := newTestPlugin()
			obs := collect(t, p, fieldsFor(s, nil))
			mustErr(t, obs, tt.metric, tt.err)
			mustValue(t, obs, "redis.up", 1)
		})
	}
}
