// Package statuspage serves the agent's local, read-only status page
// (backlog 3.132): an HTML page and the same data as /status.json, showing
// what the agent collects and sends, exactly as it sends it, and what else
// it may do on the machine. Only the host's own agent.yaml turns it on.
// The running agent hands it a Snapshot through Source; the page never
// talks to the manager and has no write action of any kind.
package statuspage

import "time"

// Source is the running agent, as the page sees it.
type Source interface {
	Snapshot() Snapshot
}

// Snapshot is everything the page shows. Its JSON form is /status.json.
type Snapshot struct {
	GeneratedAt time.Time  `json:"generated_at"`
	Agent       Agent      `json:"agent"`
	Connection  Connection `json:"connection"`
	Identity    Identity   `json:"identity"`
	// Checks are the checks the agent runs, the manager's and the local
	// agent.yaml ones, with the values last produced for the manager.
	Checks []Check `json:"checks"`
	// Facts are the facts last sent.
	Facts Facts `json:"facts"`
	// Heartbeat is the last heartbeat sent, as sent.
	Heartbeat Heartbeat `json:"heartbeat"`
	// Probes are the probes of other hosts the agent runs for the manager.
	Probes Probes `json:"probes"`
	// Abilities is what else the agent may do on the machine.
	Abilities Abilities `json:"abilities"`
	Buffer    Buffer    `json:"buffer"`
	// Log is the last lines of the agent's log, oldest first.
	Log []string `json:"log"`
}

// Agent is the running build.
type Agent struct {
	Version   string    `json:"version"`
	OS        string    `json:"os"`
	Arch      string    `json:"arch"`
	StartedAt time.Time `json:"started_at"`
}

// Connection is the agent's link to its manager.
type Connection struct {
	Manager        string     `json:"manager"`
	Connected      bool       `json:"connected"`
	ConnectedSince *time.Time `json:"connected_since"`
	LastContact    *time.Time `json:"last_contact"`
	LastHeartbeat  *time.Time `json:"last_heartbeat"`
}

// Identity is who the agent is to its manager. The names come from the
// manager; an older manager sends none, and only the IDs are known.
type Identity struct {
	AgentName    string     `json:"agent_name"`
	Hostname     string     `json:"hostname"`
	OrgID        string     `json:"org_id"`
	OrgName      string     `json:"org_name"`
	HostID       string     `json:"host_id"`
	HostName     string     `json:"host_name"`
	CertNotAfter *time.Time `json:"cert_not_after"`
	RenewalDue   *time.Time `json:"renewal_due"`
}

// Check is one check the agent runs.
type Check struct {
	Key             string `json:"key"`
	Plugin          string `json:"plugin"`
	Local           bool   `json:"local"`
	IntervalSeconds int    `json:"interval_seconds"`
	// LastRun is when its last results were taken; nil before the first.
	LastRun *time.Time `json:"last_run"`
	// Results are its last results, as produced for the manager.
	Results []Value `json:"results"`
}

// Value is one result as sent: exactly one of Value, Label or Error.
type Value struct {
	Item   string   `json:"item"`
	Metric string   `json:"metric"`
	Value  *float64 `json:"value,omitempty"`
	Label  *string  `json:"label,omitempty"`
	Error  string   `json:"error,omitempty"`
}

// Facts are the host facts last sent.
type Facts struct {
	SentAt *time.Time        `json:"sent_at"`
	Values map[string]string `json:"values"`
}

// Heartbeat is the last heartbeat as sent.
type Heartbeat struct {
	SentAt             *time.Time   `json:"sent_at"`
	Version            string       `json:"version"`
	ConfigVersion      string       `json:"config_version"`
	SecretNames        []string     `json:"secret_names"`
	SecretsState       string       `json:"secrets_state"`
	LocalChecks        []LocalCheck `json:"local_checks"`
	LocalCheckProblems []string     `json:"local_check_problems"`
	ProbesSupported    bool         `json:"probes_supported"`
	ProbeConfigVersion string       `json:"probe_config_version"`
	StatusPage         string       `json:"status_page"`
}

// LocalCheck is an agent.yaml check as the heartbeat reports it.
type LocalCheck struct {
	Key             string `json:"key"`
	Plugin          string `json:"plugin"`
	ParamsJSON      string `json:"params_json"`
	IntervalSeconds int    `json:"interval_seconds"`
}

// Probes is whether the agent probes other hosts and what.
type Probes struct {
	Enabled   bool     `json:"enabled"`
	DenyCIDRs []string `json:"deny_cidrs"`
	Assigned  []Probe  `json:"assigned"`
}

// Probe is one probe of another host the manager assigned.
type Probe struct {
	TargetHostID    string     `json:"target_host_id"`
	CheckKey        string     `json:"check_key"`
	Plugin          string     `json:"plugin"`
	Address         string     `json:"address"`
	IntervalSeconds int        `json:"interval_seconds"`
	LastRun         *time.Time `json:"last_run"`
	Results         []Value    `json:"results"`
}

// Abilities is what else the agent may do on the machine. It never runs
// playbooks or remote commands; that statement is fixed on the page.
type Abilities struct {
	// ConfigFile is agent.yaml's path and ConfigState its state ("ok",
	// "absent" or why it is refused).
	ConfigFile  string `json:"config_file"`
	ConfigState string `json:"config_state"`
	// ScriptCommands are the names of the commands script checks may run.
	ScriptCommands []string `json:"script_commands"`
	// SecretNames are the names of agent.yaml's secrets, never a value.
	SecretNames []string `json:"secret_names"`
	// LocalChecks are the keys of agent.yaml's checks.
	LocalChecks []string `json:"local_checks"`
}

// Buffer is what waits for the manager.
type Buffer struct {
	CheckResults int        `json:"check_results"`
	ProbeResults int        `json:"probe_results"`
	Oldest       *time.Time `json:"oldest"`
}

// redactedParams returns s with fn applied to the local checks' params:
// the one field the page redacts itself. Everything else the page shows
// was redacted before it was sent, or is shown as sent; the heartbeat
// sends the params as written, so a secret typed literally into a local
// check's params reaches the manager but not the page (controller ruling,
// fix round 1 of backlog 3.132).
func (s Snapshot) redactedParams(fn func(string) string) Snapshot {
	if fn == nil || len(s.Heartbeat.LocalChecks) == 0 {
		return s
	}
	local := make([]LocalCheck, len(s.Heartbeat.LocalChecks))
	for i, c := range s.Heartbeat.LocalChecks {
		c.ParamsJSON = fn(c.ParamsJSON)
		local[i] = c
	}
	s.Heartbeat.LocalChecks = local
	return s
}
