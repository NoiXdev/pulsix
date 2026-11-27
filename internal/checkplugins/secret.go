package checkplugins

// SecretParamName is the parameter a plugin that needs credentials takes
// (backlog 3.5): the name of a secret in the agent's local configuration
// file. The agent's scheduler resolves it and hands the plugin the
// secret's fields; the manager only ever sees the name.
const SecretParamName = "secret"

// SecretParam is the one spelling of that parameter: a non-empty string of
// format secretName, at most 64 bytes. A plugin embeds it in its Params,
// setting Required on its copy when it cannot run without credentials.
// UsesSecret recognises only this shape, so the manager's missing-secret
// warning and the agent's resolution always read the same parameter.
func SecretParam() ParamSpec {
	return ParamSpec{
		Name: SecretParamName, Type: TypeString, NonEmpty: true, MaxBytes: 64,
		Format: FormatSecretName, Example: "db-main",
	}
}

// UsesSecret reports whether d takes the secret parameter (SecretParam's
// name, type and format). No plugin does before 3.10.
func (d Descriptor) UsesSecret() bool {
	for _, p := range d.Params {
		if p.Name == SecretParamName {
			return p.Type == TypeString && p.Format == FormatSecretName
		}
	}
	return false
}

// SecretNameOf returns the secret a check of d with params names: the
// secret parameter's value when d uses a secret and the value is a
// non-empty string. A check without one runs without credentials.
func (d Descriptor) SecretNameOf(params map[string]any) (string, bool) {
	if !d.UsesSecret() {
		return "", false
	}
	name, ok := params[SecretParamName].(string)
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

// SendsWrites reports whether a check of d with params may change
// something on its target (d's Writes hook).
func (d Descriptor) SendsWrites(params map[string]any) bool {
	return d.Writes != nil && d.Writes(params)
}

// ProbedOnlyFromManager reports whether a check of d with params is
// probed only from the Pulsix server, never from agent vantages: it names
// a secret (whose fields never leave the server), or d's ManagerOnly hook
// says so. The validation refuses vantage groups for such a check, and
// the probe runner and its host-page view ignore any it has.
func (d Descriptor) ProbedOnlyFromManager(params map[string]any) bool {
	if _, named := d.SecretNameOf(params); named {
		return true
	}
	return d.ManagerOnly != nil && d.ManagerOnly(params) != ""
}
