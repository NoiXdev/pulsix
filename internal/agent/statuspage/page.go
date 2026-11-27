package statuspage

import (
	"html/template"
	"strconv"
	"time"
)

// refreshSeconds is how often the page reloads itself (a meta refresh: no
// script, so the CSP allows none).
const refreshSeconds = 10

// pageData is what the templates render.
type pageData struct {
	T       texts
	Nonce   string
	S       Snapshot
	Refresh int
	// Login form state.
	Failed, TooMany bool
}

// resultsData is the "results" template's input.
type resultsData struct {
	T texts
	V []Value
}

var funcs = template.FuncMap{
	"when": func(t *time.Time, never string) string {
		if t == nil || t.IsZero() {
			return never
		}
		return t.UTC().Format("2006-01-02 15:04:05 UTC")
	},
	"at": func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05 UTC") },
	"num": func(v *float64) string {
		if v == nil {
			return ""
		}
		return strconv.FormatFloat(*v, 'g', -1, 64)
	},
	"vals": func(t texts, v []Value) resultsData { return resultsData{T: t, V: v} },
	"orDash": func(s string) string {
		if s == "" {
			return "—"
		}
		return s
	},
}

const styles = `
body{font:14px/1.45 system-ui,sans-serif;margin:0 auto;max-width:72rem;padding:1rem;color:#1b1f24;background:#fff}
h1{font-size:1.4rem;margin:.2rem 0}h2{font-size:1.1rem;margin:1.6rem 0 .4rem;border-bottom:1px solid #d0d7de;padding-bottom:.2rem}
h3{font-size:.95rem;margin:1rem 0 .3rem}p.note{color:#57606a;margin:.2rem 0 .6rem}
table{border-collapse:collapse;width:100%;margin:.3rem 0}th,td{text-align:left;vertical-align:top;padding:.25rem .5rem;border-bottom:1px solid #eaeef2}
th{font-weight:600;white-space:nowrap}td{overflow-wrap:anywhere}dl{display:grid;grid-template-columns:max-content 1fr;gap:.2rem 1rem;margin:.3rem 0}
dt{font-weight:600}dd{margin:0;overflow-wrap:anywhere}code,pre{font:12px/1.4 ui-monospace,monospace}
pre{background:#f6f8fa;padding:.6rem;overflow-x:auto;white-space:pre-wrap;overflow-wrap:anywhere}
.err{color:#b42318}.muted{color:#57606a}.box{border:1px solid #d0d7de;border-radius:6px;padding:.6rem .8rem;margin:.6rem 0}
form{max-width:24rem}input{width:100%;padding:.4rem;font:inherit;box-sizing:border-box}button{margin-top:.6rem;padding:.4rem 1rem;font:inherit}
@media (prefers-color-scheme:dark){body{background:#0d1117;color:#e6edf3}th,td{border-color:#30363d}h2{border-color:#30363d}
pre{background:#161b22}.muted,p.note{color:#8d96a0}.err{color:#ff7b72}.box{border-color:#30363d}}
`

var pageTmpl = template.Must(template.New("page").Funcs(funcs).Parse(`<!doctype html>
<html lang="{{.T.Lang}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="refresh" content="{{.Refresh}}"><meta name="referrer" content="no-referrer">
<title>{{.T.Title}}</title><style nonce="{{.Nonce}}">` + styles + `</style></head><body>
{{$t := .T}}{{$s := .S}}
<h1>{{$t.Title}}</h1><p class="note">{{$t.Intro}}</p><p class="note">{{$t.Refresh}}</p>

<h2>{{$t.SecAgent}}</h2><dl>
<dt>{{$t.Version}}</dt><dd>{{orDash $s.Agent.Version}}</dd>
<dt>{{$t.System}}</dt><dd>{{$s.Agent.OS}}/{{$s.Agent.Arch}}</dd>
<dt>{{$t.Started}}</dt><dd>{{at $s.Agent.StartedAt}}</dd></dl>

<h2>{{$t.SecConnection}}</h2><dl>
<dt>{{$t.Manager}}</dt><dd><code>{{$s.Connection.Manager}}</code></dd>
<dt>{{$t.Connected}}</dt><dd>{{if $s.Connection.Connected}}{{$t.Yes}}, {{$t.Since}} {{when $s.Connection.ConnectedSince $t.Never}}{{else}}<span class="err">{{$t.No}}</span>{{end}}</dd>
<dt>{{$t.LastContact}}</dt><dd>{{when $s.Connection.LastContact $t.Never}}</dd>
<dt>{{$t.LastHeartbeat}}</dt><dd>{{when $s.Connection.LastHeartbeat $t.Never}}</dd></dl>

<h2>{{$t.SecIdentity}}</h2><dl>
<dt>{{$t.AgentName}}</dt><dd>{{orDash $s.Identity.AgentName}}</dd>
<dt>{{$t.Hostname}}</dt><dd>{{orDash $s.Identity.Hostname}}</dd>
<dt>{{$t.Organization}}</dt><dd>{{if $s.Identity.OrgName}}{{$s.Identity.OrgName}} <span class="muted">({{$s.Identity.OrgID}})</span>{{else}}{{orDash $s.Identity.OrgID}}{{end}}</dd>
<dt>{{$t.Host}}</dt><dd>{{if $s.Identity.HostName}}{{$s.Identity.HostName}} <span class="muted">({{$s.Identity.HostID}})</span>{{else}}{{orDash $s.Identity.HostID}}{{end}}</dd>
<dt>{{$t.CertValid}}</dt><dd>{{when $s.Identity.CertNotAfter "—"}}</dd>
<dt>{{$t.RenewalDue}}</dt><dd>{{when $s.Identity.RenewalDue "—"}}</dd></dl>
{{if and (not $s.Identity.OrgName) $s.Identity.OrgID}}<p class="note">{{$t.NamesUnknown}}</p>{{end}}

<h2>{{$t.SecChecks}}</h2><p class="note">{{$t.ChecksIntro}}</p>
{{range $s.Checks}}<div class="box"><h3><code>{{.Key}}</code></h3><dl>
<dt>{{$t.Plugin}}</dt><dd>{{.Plugin}}</dd>
<dt>{{$t.Source}}</dt><dd>{{if .Local}}{{$t.FromLocal}}{{else}}{{$t.FromManager}}{{end}}</dd>
<dt>{{$t.Interval}}</dt><dd>{{.IntervalSeconds}} {{$t.Seconds}}</dd>
<dt>{{$t.LastRun}}</dt><dd>{{when .LastRun $t.Never}}</dd></dl>
{{template "results" (vals $t .Results)}}</div>
{{else}}<p class="muted">{{$t.NoChecks}}</p>{{end}}

<h2>{{$t.SecAlsoSent}}</h2>
<h3>{{$t.FactsSent}}{{if $s.Facts.SentAt}} <span class="muted">({{when $s.Facts.SentAt ""}})</span>{{end}}</h3>
{{if $s.Facts.Values}}<table>{{range $k, $v := $s.Facts.Values}}<tr><th><code>{{$k}}</code></th><td>{{$v}}</td></tr>{{end}}</table>{{else}}<p class="muted">{{$t.NoFacts}}</p>{{end}}
<h3>{{$t.HeartbeatSent}}{{if $s.Heartbeat.SentAt}} <span class="muted">({{when $s.Heartbeat.SentAt ""}})</span>{{end}}</h3>
{{with $s.Heartbeat}}<dl>
<dt>{{$t.Version}}</dt><dd>{{orDash .Version}}</dd>
<dt>{{$t.ConfigVersion}}</dt><dd><code>{{orDash .ConfigVersion}}</code></dd>
<dt>{{$t.SecretNames}}</dt><dd>{{range $i, $n := .SecretNames}}{{if $i}}, {{end}}<code>{{$n}}</code>{{else}}{{$t.None}}{{end}}</dd>
<dt>{{$t.SecretsState}}</dt><dd>{{orDash .SecretsState}}</dd>
<dt>{{$t.ProbesSupported}}</dt><dd>{{if .ProbesSupported}}{{$t.Yes}}{{else}}{{$t.No}}{{end}}</dd>
<dt>{{$t.ProbeConfigVersion}}</dt><dd><code>{{orDash .ProbeConfigVersion}}</code></dd>
<dt>{{$t.StatusPageMode}}</dt><dd>{{orDash .StatusPage}}</dd></dl>
{{if .LocalChecks}}<table><tr><th>{{$t.LocalChecks}}</th><th>{{$t.Plugin}}</th><th>{{$t.Interval}}</th><th>{{$t.Params}}</th></tr>
{{range .LocalChecks}}<tr><td><code>{{.Key}}</code></td><td>{{.Plugin}}</td><td>{{.IntervalSeconds}} {{$t.Seconds}}</td><td><code>{{.ParamsJSON}}</code></td></tr>{{end}}</table>{{end}}
{{if .LocalCheckProblems}}<p>{{$t.LocalProblems}}</p><ul>{{range .LocalCheckProblems}}<li class="err">{{.}}</li>{{end}}</ul>{{end}}{{end}}

<h2>{{$t.SecProbes}}</h2>
<p>{{if $s.Probes.Enabled}}{{$t.ProbesOn}}{{else}}{{$t.ProbesOff}}{{end}}</p>
<dl><dt>{{$t.DenyRanges}}</dt><dd>{{range $i, $c := $s.Probes.DenyCIDRs}}{{if $i}}, {{end}}<code>{{$c}}</code>{{else}}{{$t.None}}{{end}}</dd></dl>
{{range $s.Probes.Assigned}}<div class="box"><h3><code>{{.CheckKey}}</code> → <code>{{.Address}}</code></h3><dl>
<dt>{{$t.Target}}</dt><dd><code>{{.TargetHostID}}</code></dd>
<dt>{{$t.Plugin}}</dt><dd>{{.Plugin}}</dd>
<dt>{{$t.Interval}}</dt><dd>{{.IntervalSeconds}} {{$t.Seconds}}</dd>
<dt>{{$t.LastRun}}</dt><dd>{{when .LastRun $t.Never}}</dd></dl>
{{template "results" (vals $t .Results)}}</div>
{{else}}{{if $s.Probes.Enabled}}<p class="muted">{{$t.NoProbes}}</p>{{end}}{{end}}

<h2>{{$t.SecMay}}</h2>
<p><strong>{{$t.NoPlaybooks}}</strong></p>
<dl><dt>{{$t.ScriptCommands}}</dt><dd>{{range $i, $c := $s.Abilities.ScriptCommands}}{{if $i}}, {{end}}<code>{{$c}}</code>{{else}}{{$t.NoScriptCommands}}{{end}}</dd>
<dt>{{$t.ConfigFile}}</dt><dd><code>{{$s.Abilities.ConfigFile}}</code></dd>
<dt>{{$t.ConfigState}}</dt><dd>{{orDash $s.Abilities.ConfigState}}</dd>
<dt>{{$t.SecretNames}}</dt><dd>{{range $i, $n := $s.Abilities.SecretNames}}{{if $i}}, {{end}}<code>{{$n}}</code>{{else}}{{$t.None}}{{end}}</dd>
<dt>{{$t.LocalCheckKeys}}</dt><dd>{{range $i, $n := $s.Abilities.LocalChecks}}{{if $i}}, {{end}}<code>{{$n}}</code>{{else}}{{$t.None}}{{end}}</dd></dl>
<p class="note">{{$t.SecretsByName}}</p>

<h2>{{$t.SecBuffer}}</h2><dl>
<dt>{{$t.BufChecks}}</dt><dd>{{$s.Buffer.CheckResults}}</dd>
<dt>{{$t.BufProbes}}</dt><dd>{{$s.Buffer.ProbeResults}}</dd>
<dt>{{$t.Oldest}}</dt><dd>{{when $s.Buffer.Oldest "—"}}</dd></dl>

<h2>{{$t.SecLog}}</h2><p class="note">{{$t.LogIntro}}</p>
<pre>{{range $s.Log}}{{.}}
{{end}}</pre>
</body></html>
{{define "results"}}{{if .V}}<table><tr><th>{{.T.Item}}</th><th>{{.T.Metric}}</th><th>{{.T.Result}}</th></tr>
{{range .V}}<tr><td>{{orDash .Item}}</td><td><code>{{.Metric}}</code></td><td>{{if .Error}}<span class="err">{{.Error}}</span>{{else if .Label}}{{.Label}}{{else}}{{num .Value}}{{end}}</td></tr>{{end}}</table>{{end}}{{end}}`))

var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="{{.T.Lang}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="referrer" content="no-referrer"><title>{{.T.Title}}</title><style nonce="{{.Nonce}}">` + styles + `</style></head><body>
<h1>{{.T.Title}}</h1><h2>{{.T.LoginTitle}}</h2><p class="note">{{.T.LoginIntro}}</p>
{{if .TooMany}}<p class="err">{{.T.LoginTooMany}}</p>{{else if .Failed}}<p class="err">{{.T.LoginFailed}}</p>{{end}}
<form method="post" action="/login"><label for="token">{{.T.Token}}</label>
<input id="token" name="token" type="password" autocomplete="current-password" required autofocus>
<button type="submit">{{.T.SignIn}}</button></form></body></html>`))
