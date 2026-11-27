package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/assert"
	"github.com/noixdev/pulsix/internal/probe/judge"
)

const (
	dnsDefaultTimeout = 5 * time.Second
	dnsDefaultPort    = 53
)

// errNameIsAddress: a dns A or AAAA check of an IP literal asks no
// server, so it cannot be attempted.
var errNameIsAddress = errors.New("name is an address")

// dialerFor returns the dialer for one connection; production passes the
// guard's Dialer, tests a dialer that refuses where the guard's up-front
// check does not.
type dialerFor func(timeout time.Duration) *net.Dialer

// probeDNS resolves name (default: the host's address) for one record type,
// through server when one is set and the manager's resolver otherwise.
// Each assertion is judged on an up lookup (backlog 3.87).
func probeDNS(ctx context.Context, g *probe.Guard, params map[string]any, address string, dial dialerFor) []probe.Observation {
	expect := probe.StrParam(params, "expect", "")
	entries := judge.Entries(assert.DNS, params)
	fail := func(msg string) []probe.Observation { return failAll(expect, entries, msg) }

	name := probe.StrParam(params, "name", probe.AddressPlaceholder)
	if strings.Contains(name, probe.AddressPlaceholder) {
		host, err := probe.TargetHost(address)
		if err != nil {
			return fail(err.Error())
		}
		name = strings.ReplaceAll(name, probe.AddressPlaceholder, host)
	}

	typ := probe.StrParam(params, "type", "A")
	lookup, ok := dnsLookupFor(typ)
	if !ok {
		return fail("unsupported record type")
	}
	// The resolver answers an A or AAAA lookup of an IP literal itself,
	// without asking any server, so such a check could never fail.
	if _, err := netip.ParseAddr(name); err == nil && (typ == "A" || typ == "AAAA") {
		return fail(errNameIsAddress.Error())
	}

	resolver := net.DefaultResolver
	// refused records a dial the guard refused: Go's resolver reports it
	// only as a failed lookup, not as probe.ErrNotAllowed.
	var refused probe.Refusal
	// connFailed records why talking to the server failed: Go's resolver
	// keeps only the text of a connection's error.
	var connFailed ioFailure
	if server := probe.StrParam(params, "server", ""); server != "" {
		ap, err := parseDNSServer(server)
		if err != nil {
			return fail(err.Error())
		}
		if err := g.Check(ap.Addr()); err != nil {
			return fail(probe.RefusalText(err))
		}
		resolver = guardedResolver(dial, ap, &refused, &connFailed)
	}

	ctx, cancel := probe.Bound(ctx, params, dnsDefaultTimeout)
	defer cancel()

	start := time.Now()
	answers, err := lookup(ctx, resolver, name)
	elapsed := time.Since(start)
	if refused.Refused() {
		return fail(refused.Text())
	}
	if err != nil || len(answers) == 0 {
		return probe.Down("dns", lookupFailure(err, connFailed.label()))
	}
	// An answer that does not match is no failure: the name resolved.
	obs := append(probe.Up("dns"), probe.Value("dns.resolve_ms", probe.Millis(elapsed)))
	if expect != "" {
		obs = append(obs, probe.BoolValue("dns.answer_match", answerMatches(answers, expect)))
	}
	r := lookupResult{typ: typ, answers: answers, elapsed: elapsed}
	return append(obs, judge.Observe("dns", entries, r.judge)...)
}

// hostMetrics are the whole-host metrics a probe with an expect of
// expect reports on an up attempt.
func hostMetrics(expect string) []string {
	metrics := []string{"dns.up", "dns.failure", "dns.resolve_ms"}
	if expect != "" {
		metrics = append(metrics, "dns.answer_match")
	}
	return metrics
}

// FailAll is the result of a dns probe of params that could not be
// attempted: msg on its whole-host metrics and on both metrics of each
// assertion's item.
func FailAll(params map[string]any, msg string) []probe.Observation {
	return failAll(probe.StrParam(params, "expect", ""), judge.Entries(assert.DNS, params), msg)
}

func failAll(expect string, entries []judge.Entry, msg string) []probe.Observation {
	return append(probe.FailAll(msg, hostMetrics(expect)...), judge.FailAll("dns", entries, msg)...)
}

// lookupResult is what an up lookup found (backlog 3.87).
type lookupResult struct {
	typ     string
	answers []string
	elapsed time.Duration
}

// judge evaluates one of dns's subjects.
func (r lookupResult) judge(a assert.Assertion, w *judge.Work) judge.Verdict {
	switch a.Subject() {
	case assert.SubjectResolveMS:
		return judge.Number(a, probe.Millis(r.elapsed))
	case assert.SubjectAnswerCount:
		return judge.Number(a, float64(len(r.answers)))
	case assert.SubjectAnswers:
		return w.Text(a, answersText(r.typ, r.answers), true)
	case assert.SubjectAnswer:
		found, ok := matchingAnswer(r.typ, r.answers, a.Arg())
		return w.Text(a, found, ok)
	}
	return judge.Failed(judge.ErrInvalid)
}

// displayAnswer is an answer as the assertions show it: a name (CNAME,
// MX, NS) lower-cased without its trailing dot, an address and a TXT
// string as they came (addresses are canonical already, TXT is case
// sensitive).
func displayAnswer(typ, answer string) string {
	switch typ {
	case "CNAME", "MX", "NS":
		return normalizeAnswer(answer)
	}
	return answer
}

// answersText is the answers as displayAnswer shows them, sorted and
// joined with ", ", so an assertion does not depend on the server's
// order.
func answersText(typ string, answers []string) string {
	shown := make([]string, len(answers))
	for i, a := range answers {
		shown[i] = displayAnswer(typ, a)
	}
	slices.Sort(shown)
	return strings.Join(shown, ", ")
}

// matchingAnswer is the first answer that equals want by answerMatches'
// comparison, as displayAnswer shows it.
func matchingAnswer(typ string, answers []string, want string) (string, bool) {
	for _, a := range answers {
		if answerMatches([]string{a}, want) {
			return displayAnswer(typ, a), true
		}
	}
	return "", false
}

// guardedResolver sends every query to server, through the guard's dialer,
// and notes in refused when that dialer refuses. The guard was already asked
// about server; the dial-time check is the backstop. Each connection's
// failures are noted in failed.
func guardedResolver(dial dialerFor, server netip.AddrPort, refused *probe.Refusal, failed *ioFailure) *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			conn, err := dial(0).DialContext(ctx, network, server.String())
			refused.Note(err)
			if err != nil {
				failed.note(err)
				return nil, err
			}
			return failed.watch(conn), nil
		},
	}
}

type dnsLookup func(ctx context.Context, r *net.Resolver, name string) ([]string, error)

// dnsLookupFor returns the lookup for a record type. A and AAAA query only
// their own family, so a server that ignores the other family's query does
// not stall the probe until its timeout.
func dnsLookupFor(typ string) (dnsLookup, bool) {
	switch typ {
	case "A":
		return func(ctx context.Context, r *net.Resolver, name string) ([]string, error) {
			return lookupIP(ctx, r, "ip4", name)
		}, true
	case "AAAA":
		return func(ctx context.Context, r *net.Resolver, name string) ([]string, error) {
			return lookupIP(ctx, r, "ip6", name)
		}, true
	case "CNAME":
		return func(ctx context.Context, r *net.Resolver, name string) ([]string, error) {
			cname, err := r.LookupCNAME(ctx, name)
			if err != nil || cname == "" {
				return nil, err
			}
			return []string{cname}, nil
		}, true
	case "MX":
		return func(ctx context.Context, r *net.Resolver, name string) ([]string, error) {
			mxs, err := r.LookupMX(ctx, name)
			out := make([]string, 0, len(mxs))
			for _, mx := range mxs {
				out = append(out, mx.Host)
			}
			return out, err
		}, true
	case "TXT":
		return func(ctx context.Context, r *net.Resolver, name string) ([]string, error) {
			return r.LookupTXT(ctx, name)
		}, true
	case "NS":
		return func(ctx context.Context, r *net.Resolver, name string) ([]string, error) {
			nss, err := r.LookupNS(ctx, name)
			out := make([]string, 0, len(nss))
			for _, ns := range nss {
				out = append(out, ns.Host)
			}
			return out, err
		}, true
	}
	return nil, false
}

func lookupIP(ctx context.Context, r *net.Resolver, network, name string) ([]string, error) {
	ips, err := r.LookupNetIP(ctx, network, name)
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.Unmap().String())
	}
	return out, err
}

// parseDNSServer parses the server param: an IP with an optional port.
func parseDNSServer(s string) (netip.AddrPort, error) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		if ap.Port() == 0 {
			return netip.AddrPort{}, fmt.Errorf("invalid dns server %q", s)
		}
		return ap, nil
	}
	ip, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"))
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("invalid dns server %q", s)
	}
	return netip.AddrPortFrom(ip, dnsDefaultPort), nil
}

// answerMatches reports whether one answer equals expect, compared
// case-insensitively without a trailing dot; addresses compare as addresses,
// so any spelling of the same IPv6 address matches.
func answerMatches(answers []string, expect string) bool {
	want := normalizeAnswer(expect)
	wantIP, wantErr := netip.ParseAddr(want)
	for _, a := range answers {
		got := normalizeAnswer(a)
		if got == want {
			return true
		}
		if wantErr == nil {
			if ip, err := netip.ParseAddr(got); err == nil && ip.Unmap() == wantIP.Unmap() {
				return true
			}
		}
	}
	return false
}

func normalizeAnswer(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

// Probe is the dns probe, as the registry runs it.
func Probe(ctx context.Context, g *probe.Guard, params map[string]any, address string, _ map[string]string) []probe.Observation {
	return probeDNS(ctx, g, params, address, g.Dialer)
}
