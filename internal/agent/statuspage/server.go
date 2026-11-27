package statuspage

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/net/netutil"
)

// Server limits (R9 of the 3.132 plan): the page must not become a way to
// slow the agent's monitoring.
const (
	maxConns          = 16
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 16 << 10
)

// Mode is what the page is doing, as the heartbeat reports it.
type Mode int

// The modes.
const (
	ModeOff Mode = iota
	ModeLoopback
	ModeNetwork
	// ModeFailed: agent.yaml asks for the page, but its settings are
	// refused or the page could not start.
	ModeFailed
)

// Config is agent.yaml's status_page section, as the controller applies
// it.
type Config struct {
	Listen, Token, TLSCert, TLSKey string
	Network                        bool
}

// Start limits (review 3.132, finding 4): a start runs off the caller's
// path, bounded to startTimeout (a name that does not resolve), and a
// failed one is tried again at most once per retryEvery.
const (
	startTimeout = time.Second
	retryEvery   = time.Minute
)

// Options are the controller's dependencies.
type Options struct {
	// DataDir is where a network page's self-signed certificate is kept.
	DataDir string
	// Redact replaces secret values in the local checks' params the page
	// shows.
	Redact func(string) string
	Log    *slog.Logger
	Now    func() time.Time
	// Hostname is the machine's name for the self-signed certificate;
	// nil is os.Hostname.
	Hostname func() (string, error)
	// Listen opens the listener; nil is net.ListenConfig.Listen. Tests
	// inject one that never answers.
	Listen func(ctx context.Context, network, addr string) (net.Listener, error)
}

// Controller owns the page's listener: Apply starts, moves or stops it
// to follow agent.yaml, Close stops it for good. Starting happens on a
// goroutine of its own, so Apply never waits on a listener or a name
// lookup; Mode reports what is served. It is safe for concurrent use.
type Controller struct {
	src  Source
	opts Options
	wg   sync.WaitGroup // start attempts in flight
	// startMu serialises start attempts across opening the listener and
	// deciding what to do with it, so a stale attempt has closed its
	// listener before the next one binds the same port (re-review 3.132,
	// finding 1). It is taken before mu, never while mu is held.
	startMu sync.Mutex

	mu      sync.Mutex
	closed  bool
	want    Config
	problem string
	// gen counts changes of what should be served; an attempt started for
	// an older gen closes what it opened.
	gen        int
	attempting bool // an attempt for gen is in flight
	lastTry    time.Time
	srv        *http.Server
	ln         net.Listener
	// certUntil is the self-signed certificate's end, zero when the page
	// serves none.
	certUntil time.Time
	mode      Mode
	lastErr   string
}

// NewController wires a controller; the page is off until Apply.
func NewController(src Source, opts Options) *Controller {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Hostname == nil {
		opts.Hostname = os.Hostname
	}
	if opts.Listen == nil {
		var lc net.ListenConfig
		opts.Listen = lc.Listen
	}
	return &Controller{src: src, opts: opts}
}

// Apply makes the page follow cfg: off when cfg has no listen address,
// failed when problem is set (the section was refused; the watcher logged
// why), started on the new address when cfg changed, left alone when it
// did not. A page that failed to start is tried again at most once a
// minute, and its failure is logged once per change. A self-signed
// certificate near its end is replaced. Apply never waits for a start.
func (c *Controller) Apply(cfg Config, problem string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	now := c.opts.Now()
	switch {
	case problem != "":
		c.resetLocked(ModeFailed, problem)
	case cfg.Listen == "":
		if c.srv != nil || c.mode != ModeOff {
			c.opts.Log.Info("the status page is off")
		}
		c.resetLocked(ModeOff, "")
	case cfg != c.want || c.problem != "":
		c.resetLocked(ModeOff, "")
		c.want = cfg
		c.startLocked(now)
	case c.srv != nil:
		if !c.certUntil.IsZero() && c.certUntil.Sub(now) <= selfSignedRenew {
			c.resetLocked(ModeOff, "")
			c.want = cfg
			c.startLocked(now)
		}
	case !c.attempting && now.Sub(c.lastTry) >= retryEvery:
		c.startLocked(now)
	}
}

// resetLocked stops what is served and forgets what should be; c.mu is
// held.
func (c *Controller) resetLocked(mode Mode, problem string) {
	c.stopLocked()
	c.gen++
	c.want, c.problem, c.mode, c.attempting = Config{}, problem, mode, false
	if mode == ModeOff || problem != "" {
		c.lastErr = ""
	}
}

// startLocked starts an attempt to serve c.want; c.mu is held.
func (c *Controller) startLocked(now time.Time) {
	gen, cfg := c.gen, c.want
	c.attempting, c.lastTry = true, now
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.startMu.Lock()
		defer c.startMu.Unlock()
		if !c.current(gen) {
			return // a newer change came while this one waited
		}
		ln, until, err := c.open(cfg, now)
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed || gen != c.gen {
			if ln != nil {
				_ = ln.Close()
			}
			return
		}
		c.attempting = false
		if err != nil {
			c.mode = ModeFailed
			if msg := err.Error(); msg != c.lastErr {
				c.lastErr = msg
				c.opts.Log.Warn("the status page could not start; it is tried again at most once a minute",
					"network", cfg.Network, "err", err)
			}
			return
		}
		c.serveLocked(cfg, ln, until)
	}()
}

// current reports whether gen is still what should be served.
func (c *Controller) current(gen int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && gen == c.gen
}

// open builds the TLS settings and opens the listener, bounded to
// startTimeout; until is the self-signed certificate's end, zero for none.
func (c *Controller) open(cfg Config, now time.Time) (ln net.Listener, until time.Time, err error) {
	var tlsCfg *tls.Config
	switch {
	case cfg.TLSCert != "":
		cert, err := loadGiven(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return nil, time.Time{}, err
		}
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	case cfg.Network:
		host, _, _ := net.SplitHostPort(cfg.Listen)
		hostname, _ := c.opts.Hostname()
		cert, err := selfSigned(c.opts.DataDir, host, hostname, now)
		if err != nil {
			return nil, time.Time{}, err
		}
		if cert.Leaf != nil {
			until = cert.Leaf.NotAfter
		}
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	ln, err = c.opts.Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return nil, time.Time{}, err
	}
	ln = netutil.LimitListener(ln, maxConns)
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	return ln, until, nil
}

// serveLocked serves the page on ln; c.mu is held.
func (c *Controller) serveLocked(cfg Config, ln net.Listener, until time.Time) {
	srv := &http.Server{
		Handler:           newHandler(c.src, cfg.Token, c.opts.Redact, c.opts.Now, c.opts.Log),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		// TLS handshake noise from scanners would flood the agent's log.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	c.srv, c.ln, c.certUntil, c.lastErr = srv, ln, until, ""
	c.mode = ModeLoopback
	if cfg.Network {
		c.mode = ModeNetwork
	}
	go func() { _ = srv.Serve(ln) }()
	c.opts.Log.Info("the status page is on", "addr", ln.Addr().String(), "network", cfg.Network,
		"https", cfg.Network || cfg.TLSCert != "", "token", cfg.Token != "")
}

// stopLocked closes the server and its listener, if any; c.mu is held.
// The listener is closed too: a stop that comes before the Serve
// goroutine has picked it up would otherwise leave the port held.
func (c *Controller) stopLocked() {
	if c.srv != nil {
		_ = c.srv.Close()
		_ = c.ln.Close()
		c.srv, c.ln, c.certUntil = nil, nil, time.Time{}
	}
}

// AwaitStart waits for the start attempts in flight, each bounded to about
// startTimeout plus a certificate: the agent calls it once at start, so
// its first heartbeat reports what is served.
func (c *Controller) AwaitStart() { c.wg.Wait() }

// settle is AwaitStart for tests.
func (c *Controller) settle() { c.AwaitStart() }

// Asked reports whether the last Apply asked for the page: a listen
// address, or a refused section.
func (c *Controller) Asked() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.want.Listen != "" || c.problem != ""
}

// Mode is what the page does now; while a start is in flight, the mode
// of the requested config (re-review 3.132, finding 2), so a heartbeat in
// that moment does not report the page off.
func (c *Controller) Mode() Mode {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.attempting {
		if c.want.Network {
			return ModeNetwork
		}
		return ModeLoopback
	}
	return c.mode
}

// Addr is the address the page listens on, "" when it is not on.
func (c *Controller) Addr() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ln == nil {
		return ""
	}
	return c.ln.Addr().String()
}

// Close stops the page for good and waits for a start in flight (at most
// about startTimeout); a later Apply does nothing.
func (c *Controller) Close() {
	c.mu.Lock()
	c.stopLocked()
	c.gen++
	c.closed, c.mode = true, ModeOff
	c.mu.Unlock()
	c.wg.Wait()
}
