package statuspage

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// texts are the page's words in one language.
type texts struct {
	Lang, Title, Intro                                                string
	SecAgent, Version, System, Started                                string
	SecConnection, Manager, Connected, Yes, No, Since, LastContact    string
	LastHeartbeat, Never                                              string
	SecIdentity, AgentName, Hostname, Organization, Host, CertValid   string
	RenewalDue, NamesUnknown                                          string
	SecChecks, ChecksIntro, Key, Plugin, Interval, LastRun, Values    string
	FromManager, FromLocal, Source, NoChecks, Item, Metric, Result    string
	SecAlsoSent, FactsSent, NoFacts, HeartbeatSent, ConfigVersion     string
	SecretsState, LocalChecks, LocalProblems, ProbesSupported         string
	ProbeConfigVersion, StatusPageMode, Params                        string
	SecProbes, ProbesOn, ProbesOff, DenyRanges, None, Target, Address string
	NoProbes                                                          string
	SecMay, NoPlaybooks, ScriptCommands, NoScriptCommands, ConfigFile string
	ConfigState, SecretNames, SecretsByName, LocalCheckKeys           string
	SecBuffer, BufChecks, BufProbes, Oldest                           string
	SecLog, LogIntro, Seconds, Refresh                                string
	LoginTitle, LoginIntro, Token, SignIn, LoginFailed, LoginTooMany  string
	Error                                                             string
}

var english = texts{
	Lang: "en", Title: "Pulsix agent status",
	Intro:    "What this agent collects and sends to its manager, exactly as it sends it, and what else it may do on this machine. This page is read-only.",
	SecAgent: "Agent", Version: "Version", System: "System", Started: "Started",
	SecConnection: "Connection", Manager: "Manager", Connected: "Connected", Yes: "yes", No: "no", Since: "since",
	LastContact: "Last contact", LastHeartbeat: "Last heartbeat", Never: "never",
	SecIdentity: "Identity", AgentName: "Agent name", Hostname: "Local host name", Organization: "Organization",
	Host: "Host", CertValid: "Certificate valid until", RenewalDue: "Next renewal from",
	NamesUnknown: "The manager has not named the organization and host yet; their IDs are shown.",
	SecChecks:    "What it collects",
	ChecksIntro:  "Every check this agent runs, with the values last produced for the manager. Errors are shown as sent; nothing is shortened.",
	Key:          "Check", Plugin: "Plugin", Interval: "Interval", LastRun: "Last run", Values: "Values",
	FromManager: "from the manager", FromLocal: "from agent.yaml", Source: "Defined", NoChecks: "No checks run.",
	Item: "Item", Metric: "Metric", Result: "Value",
	SecAlsoSent: "Also sent", FactsSent: "Host facts, last sent", NoFacts: "No facts sent yet.",
	HeartbeatSent: "Last heartbeat, as sent", ConfigVersion: "Configuration version", SecretsState: "agent.yaml state",
	LocalChecks: "Checks from agent.yaml", LocalProblems: "agent.yaml problems", ProbesSupported: "Probes other hosts",
	ProbeConfigVersion: "Probe configuration version", StatusPageMode: "Status page", Params: "Parameters",
	SecProbes: "Probes of other hosts", ProbesOn: "This agent probes other hosts for the manager.",
	ProbesOff: "This agent does not probe other hosts.", DenyRanges: "Address ranges it never reaches", None: "none",
	Target: "Target host", Address: "Address", NoProbes: "No probes assigned.",
	SecMay:         "What else it may do",
	NoPlaybooks:    "The agent itself never runs playbooks or remote commands. Server management in Pulsix connects to this machine separately, over SSH; this agent cannot see whether that happens.",
	ScriptCommands: "Commands script checks may run", NoScriptCommands: "No script checks are configured.",
	ConfigFile: "Local configuration file", ConfigState: "State", SecretNames: "Secrets (names only)",
	SecretsByName:  "Secret values are never shown; anywhere they would appear they read [secret].",
	LocalCheckKeys: "Checks defined there",
	SecBuffer:      "Waiting for the manager", BufChecks: "Check results", BufProbes: "Probe results", Oldest: "Oldest",
	SecLog: "Log", LogIntro: "The last 100 lines of the agent's log.", Seconds: "s",
	Refresh:    "This page refreshes itself every 10 seconds.",
	LoginTitle: "Sign in", LoginIntro: "This status page needs the token from the agent's agent.yaml.",
	Token: "Token", SignIn: "Sign in", LoginFailed: "That token is not right.",
	LoginTooMany: "Too many wrong tokens. Wait a minute and try again.", Error: "Error",
}

var german = texts{
	Lang: "de", Title: "Pulsix-Agent-Status",
	Intro:    "Was dieser Agent erhebt und an seinen Manager sendet, genau so, wie er es sendet, und was er auf diesem Rechner sonst darf. Diese Seite ist nur lesbar.",
	SecAgent: "Agent", Version: "Version", System: "System", Started: "Gestartet",
	SecConnection: "Verbindung", Manager: "Manager", Connected: "Verbunden", Yes: "ja", No: "nein", Since: "seit",
	LastContact: "Letzter Kontakt", LastHeartbeat: "Letzter Heartbeat", Never: "nie",
	SecIdentity: "Identität", AgentName: "Agent-Name", Hostname: "Lokaler Hostname", Organization: "Organisation",
	Host: "Host", CertValid: "Zertifikat gültig bis", RenewalDue: "Nächste Erneuerung ab",
	NamesUnknown: "Der Manager hat Organisation und Host noch nicht benannt; ihre IDs werden angezeigt.",
	SecChecks:    "Was er erhebt",
	ChecksIntro:  "Jeder Check, den dieser Agent ausführt, mit den zuletzt für den Manager erzeugten Werten. Fehler stehen so da, wie sie gesendet werden; nichts wird gekürzt.",
	Key:          "Check", Plugin: "Plugin", Interval: "Intervall", LastRun: "Zuletzt", Values: "Werte",
	FromManager: "vom Manager", FromLocal: "aus agent.yaml", Source: "Definiert", NoChecks: "Es laufen keine Checks.",
	Item: "Element", Metric: "Metrik", Result: "Wert",
	SecAlsoSent: "Außerdem gesendet", FactsSent: "Host-Fakten, zuletzt gesendet", NoFacts: "Noch keine Fakten gesendet.",
	HeartbeatSent: "Letzter Heartbeat, wie gesendet", ConfigVersion: "Konfigurationsversion", SecretsState: "Zustand der agent.yaml",
	LocalChecks: "Checks aus agent.yaml", LocalProblems: "Probleme in agent.yaml", ProbesSupported: "Prüft andere Hosts",
	ProbeConfigVersion: "Probe-Konfigurationsversion", StatusPageMode: "Statusseite", Params: "Parameter",
	SecProbes: "Prüfungen anderer Hosts", ProbesOn: "Dieser Agent prüft für den Manager andere Hosts.",
	ProbesOff: "Dieser Agent prüft keine anderen Hosts.", DenyRanges: "Adressbereiche, die er nie erreicht", None: "keine",
	Target: "Ziel-Host", Address: "Adresse", NoProbes: "Keine Prüfungen zugewiesen.",
	SecMay:         "Was er sonst darf",
	NoPlaybooks:    "Der Agent selbst führt nie Playbooks oder entfernte Befehle aus. Die Serververwaltung in Pulsix verbindet sich getrennt davon per SSH mit diesem Rechner; dieser Agent kann nicht sehen, ob das geschieht.",
	ScriptCommands: "Befehle, die Skript-Checks ausführen dürfen", NoScriptCommands: "Es sind keine Skript-Checks eingerichtet.",
	ConfigFile: "Lokale Konfigurationsdatei", ConfigState: "Zustand", SecretNames: "Geheimnisse (nur Namen)",
	SecretsByName:  "Werte von Geheimnissen werden nie gezeigt; wo sie stünden, steht [secret].",
	LocalCheckKeys: "Dort definierte Checks",
	SecBuffer:      "Wartet auf den Manager", BufChecks: "Check-Ergebnisse", BufProbes: "Prüfergebnisse", Oldest: "Ältestes",
	SecLog: "Log", LogIntro: "Die letzten 100 Zeilen des Agent-Logs.", Seconds: "s",
	Refresh:    "Diese Seite lädt sich alle 10 Sekunden neu.",
	LoginTitle: "Anmelden", LoginIntro: "Diese Statusseite braucht das Token aus der agent.yaml des Agents.",
	Token: "Token", SignIn: "Anmelden", LoginFailed: "Dieses Token stimmt nicht.",
	LoginTooMany: "Zu viele falsche Tokens. Bitte eine Minute warten.", Error: "Fehler",
}

// textsFor picks German or English from Accept-Language: the supported
// language with the highest weight, English when neither is asked for.
func textsFor(r *http.Request) texts {
	type pref struct {
		lang string
		q    float64
	}
	var prefs []pref
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		base, _, _ := strings.Cut(strings.ToLower(tag), "-")
		if (base == "de" || base == "en") && q > 0 {
			prefs = append(prefs, pref{base, q})
		}
	}
	sort.SliceStable(prefs, func(a, b int) bool { return prefs[a].q > prefs[b].q })
	if len(prefs) > 0 && prefs[0].lang == "de" {
		return german
	}
	return english
}
