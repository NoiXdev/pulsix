package statuspage

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Session cookie and failure limits (R8 of the 3.132 plan).
const (
	cookieName      = "pulsix_status"
	sessionLifetime = 12 * time.Hour
	maxFailures     = 5
	failureWindow   = time.Minute
	maxTracked      = 4096
)

// auth checks a visitor against the page's token. The zero token lets
// everyone in (a loopback page without one).
type auth struct {
	token string
	now   func() time.Time
	fails *failureLimiter
}

func newAuth(token string, now func() time.Time) *auth {
	return &auth{token: token, now: now, fails: newFailureLimiter(now)}
}

// open reports whether the page needs no token.
func (a *auth) open() bool { return a.token == "" }

// tokenOK compares a presented token in constant time: both sides are
// hashed first, so neither the length nor a prefix leaks through timing.
func (a *auth) tokenOK(presented string) bool {
	want := sha256.Sum256([]byte(a.token))
	got := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(want[:], got[:]) == 1
}

// sessionValue is a cookie valid until exp: the expiry and an HMAC of it
// keyed by the token, so a token change ends every session.
func (a *auth) sessionValue(exp time.Time) string {
	e := strconv.FormatInt(exp.Unix(), 10)
	return e + "." + a.mac(e)
}

func (a *auth) mac(e string) string {
	m := hmac.New(sha256.New, []byte(a.token))
	m.Write([]byte("pulsix-status-session\x00" + e))
	return hex.EncodeToString(m.Sum(nil))
}

// sessionOK checks a cookie value: a good MAC and an expiry in the future.
func (a *auth) sessionOK(v string) bool {
	e, mac, ok := strings.Cut(v, ".")
	if !ok {
		return false
	}
	if !hmac.Equal([]byte(mac), []byte(a.mac(e))) {
		return false
	}
	unix, err := strconv.ParseInt(e, 10, 64)
	return err == nil && a.now().Before(time.Unix(unix, 0))
}

// authorized reports whether r may see the page: no token needed, a good
// bearer token, or a good session cookie. A bad bearer token counts as a
// failure.
func (a *auth) authorized(r *http.Request) bool {
	if a.open() {
		return true
	}
	if h := r.Header.Get("Authorization"); h != "" {
		scheme, tok, _ := strings.Cut(h, " ")
		if strings.EqualFold(scheme, "Bearer") && a.tokenOK(strings.TrimSpace(tok)) {
			return true
		}
		a.fails.fail(failureKey(remoteIP(r)))
		return false
	}
	if c, err := r.Cookie(cookieName); err == nil && a.sessionOK(c.Value) {
		return true
	}
	return false
}

// sessionCookie is the cookie a good login sets.
func (a *auth) sessionCookie(secure bool) *http.Cookie {
	return &http.Cookie{
		Name: cookieName, Value: a.sessionValue(a.now().Add(sessionLifetime)), Path: "/",
		MaxAge: int(sessionLifetime / time.Second), HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
	}
}

// remoteIP is r's peer address without its port.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// failureKey is the limiter's key for a peer address: an IPv4 address as
// it is, an IPv6 address by its /64, which one host commonly holds whole
// (review 3.132, finding 6).
func failureKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	if a = a.Unmap(); a.Is4() {
		return a.String()
	}
	p, err := a.WithZone("").Prefix(64)
	if err != nil {
		return ip
	}
	return p.String()
}

// failureLimiter counts failed tokens per remote address (failureKey): after
// maxFailures within failureWindow, the address is refused until the
// window that began with its first failure is over.
type failureLimiter struct {
	now func() time.Time
	mu  sync.Mutex
	m   map[string]*failures
}

type failures struct {
	since time.Time
	n     int
}

func newFailureLimiter(now func() time.Time) *failureLimiter {
	return &failureLimiter{now: now, m: map[string]*failures{}}
}

// overflowKey counts the failures of every address that found the table
// full, together: rotating addresses must not buy unlimited tries. While
// the table is full of live entries (4096 addresses or IPv6 /64s, each
// with a failure in the last minute), five failures from any of the
// others block every address not in the table for the rest of that
// minute, legitimate visitors included; the agent's monitoring is not
// affected, only the page (documented, R17 of the 3.132 plan).
const overflowKey = "*"

// blocked reports whether ip is refused right now.
func (l *failureLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := ip
	if l.m[key] == nil {
		key = overflowKey
	}
	f := l.m[key]
	if f == nil {
		return false
	}
	if l.now().Sub(f.since) >= failureWindow {
		delete(l.m, key)
		return false
	}
	return f.n >= maxFailures
}

// fail counts one failure of ip.
func (l *failureLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	f := l.m[ip]
	if f == nil || now.Sub(f.since) >= failureWindow {
		if len(l.m) >= maxTracked {
			l.pruneLocked(now)
		}
		if len(l.m) >= maxTracked {
			ip = overflowKey // full of live entries: share one bucket
			if f = l.m[ip]; f != nil && now.Sub(f.since) < failureWindow {
				f.n++
				return
			}
		}
		f = &failures{since: now}
		l.m[ip] = f
	}
	f.n++
}

func (l *failureLimiter) pruneLocked(now time.Time) {
	for ip, f := range l.m {
		if now.Sub(f.since) >= failureWindow {
			delete(l.m, ip)
		}
	}
}
