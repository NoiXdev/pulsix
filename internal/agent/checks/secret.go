package checks

import (
	"context"
	"errors"
	"log/slog"
	"maps"
)

// SecretParam is the check parameter that names the secret a SecretPlugin
// collects with: the name of a secret in the agent's local configuration
// file (agent.yaml), never the secret itself.
const SecretParam = "secret"

// SecretPlugin is a Plugin that needs credentials kept on the host
// (backlog 3.5). The scheduler calls CollectWithSecret instead of Collect:
// it reads the check's SecretParam, looks that secret up in the agent's
// configuration file and hands the plugin a copy of its fields, alongside
// the check's params, which it leaves exactly as they are. Without a
// SecretParam the plugin is called with nil fields and decides what that
// means. The plugin never reads the file itself.
//
// The fields are for the plugin's own use only: it must not put a field
// value into an observation (an Err text included) or its advisory error.
// The scheduler hands them to nothing else.
//
// When the check names a secret the file does not define, the plugin is
// not called; the scheduler reports one error observation on item "" and
// SecretErrorMetric instead, every round until the secret appears.
// SecretErrorMetric is the plugin's host-level metric, the one a failed
// connection would report on, and must not be empty.
type SecretPlugin interface {
	Plugin
	CollectWithSecret(ctx context.Context, params map[string]any, fields map[string]string) ([]Observation, error)
	SecretErrorMetric() string
}

// Secrets is where the scheduler looks a secret up: the agent's secrets
// file watcher, adapted in internal/agent. ok is false when the file does
// not define name; problem then says why the file has no secrets at all
// (missing, refused, broken), or is empty when the file is fine and only
// lacks this one. The scheduler copies fields before handing them on.
type Secrets interface {
	Secret(name string) (fields map[string]string, ok bool, problem string)
}

// SchedulerOption configures a Scheduler.
type SchedulerOption func(*Scheduler)

// WithSecrets is where SecretPlugins' secrets come from. Without it the
// scheduler knows no secret.
func WithSecrets(s Secrets) SchedulerOption { return func(sc *Scheduler) { sc.secrets = s } }

// WithLogger is where the scheduler logs plugins' advisory errors (advisory.go).
// Without it nothing is logged.
func WithLogger(l *slog.Logger) SchedulerOption { return func(sc *Scheduler) { sc.log = l } }

// errSecretParam is the error for a SecretParam that is not a string; the
// manager validates params, so only a manager bug produces it.
var errSecretParam = errors.New(`param "secret" must be a string`)

// collect runs plugin once for one check. A CommandPlugin is handed the
// commands lookup. A SecretPlugin is handed its secret's fields, or not
// called at all when the secret cannot be resolved. The returned error is
// the plugin's advisory error (Plugin): it is returned only for a plain
// plugin, because a CommandPlugin's may carry a command argument and a
// SecretPlugin's driver text; theirs are dropped here. The observations
// carry the failures that matter.
func (s *Scheduler) collect(ctx context.Context, plugin Plugin, params map[string]any) ([]Observation, error) {
	if cp, ok := plugin.(CommandPlugin); ok {
		obs, _ := cp.CollectWithCommands(ctx, params, s.commands)
		return obs, nil
	}
	sp, ok := plugin.(SecretPlugin)
	if !ok {
		return plugin.Collect(ctx, params)
	}
	fields, err := s.secretFields(params)
	if err != nil {
		return []Observation{ErrObservation("", sp.SecretErrorMetric(), err)}, nil
	}
	return collectSecret(ctx, sp, params, fields), nil
}

// collectSecret calls sp.CollectWithSecret and turns a panic into one fixed
// error observation. The panic value and stack are dropped on purpose: a
// plugin holding credentials may have them in either, and nothing here
// logs or reports them. The recovery covers the plugin's own goroutine only:
// a panic in a goroutine a driver starts internally ends the agent, and the
// runtime's panic output prints no field values.
func collectSecret(ctx context.Context, sp SecretPlugin, params map[string]any, fields map[string]string) (obs []Observation) {
	defer func() {
		if recover() != nil {
			obs = []Observation{ErrObservation("", sp.SecretErrorMetric(), errors.New(sp.Name()+": collection failed unexpectedly"))}
		}
	}()
	obs, _ = sp.CollectWithSecret(ctx, params, fields)
	return obs
}

// secretFields resolves the secret params names: nil without a
// SecretParam, a copy of its fields when the file defines it, and the
// error to report otherwise. The error names the secret, never a field.
func (s *Scheduler) secretFields(params map[string]any) (map[string]string, error) {
	v := params[SecretParam]
	if v == nil {
		return nil, nil
	}
	name, ok := v.(string)
	if !ok {
		return nil, errSecretParam
	}
	if s.secrets == nil {
		return nil, notDefined(name, "")
	}
	fields, ok, problem := s.secrets.Secret(name)
	if !ok {
		return nil, notDefined(name, problem)
	}
	return maps.Clone(fields), nil
}

// notDefined is the error for a secret the file does not define, with the
// reason the file has none when there is one.
func notDefined(name, problem string) error {
	msg := "secret `" + name + "` is not defined in agent.yaml"
	if problem != "" {
		msg += " (" + problem + ")"
	}
	return errors.New(msg)
}
