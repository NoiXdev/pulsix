package statuspage

import (
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

// Options are the controller's dependencies.
type Options struct {
	// DataDir is where a network page's self-signed certificate is kept.
	DataDir string
	// Redact replaces secret values in everything the page shows.
	Redact func(string) string
	Log    *slog.Logger
	Now    func() time.Time
	// Hostname is the machine's name for the self-signed certificate;
	// nil is os.Hostname.
	Hostname func() (string, error)
}

// Controller owns the page's listener: Apply starts, moves or stops it
// to follow agent.yaml, Close stops it for good. It is safe for
// concurrent use.
type Controller struct {
	src  Source
	opts Options

	mu      sync.Mutex
	closed  bool
	cur     Config
	problem string
	srv     *http.Server
	ln      net.Listener
	mode    Mode
	lastErr string
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
	return &Controller{src: src, opts: opts}
}

// Apply makes the page follow cfg: off when cfg has no listen address or
// problem is set (the section was refused; the watcher logged why), on
// the new address when cfg changed, left alone when it did not. A page
// that failed to start is tried again on every Apply, and its failure is
// logged once per change.
func (c *Controller) Apply(cfg Config, problem string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if problem != "" {
		c.stopLocked()
		c.cur, c.problem, c.mode, c.lastErr = Config{}, problem, ModeFailed, ""
		return
	}
	if cfg.Listen == "" {
		if c.srv != nil || c.mode != ModeOff {
			c.opts.Log.Info("the status page is off")
		}
		c.stopLocked()
		c.cur, c.problem, c.mode, c.lastErr = Config{}, "", ModeOff, ""
		return
	}
	if cfg == c.cur && c.problem == "" && c.srv != nil {
		return
	}
	c.stopLocked()
	c.cur, c.problem = cfg, ""
	if err := c.startLocked(cfg); err != nil {
		c.mode = ModeFailed
		if msg := err.Error(); msg != c.lastErr {
			c.lastErr = msg
			c.opts.Log.Warn("the status page could not start; it is tried again with the next look at agent.yaml",
				"network", cfg.Network, "err", err)
		}
		return
	}
	c.lastErr = ""
	c.mode = ModeLoopback
	if cfg.Network {
		c.mode = ModeNetwork
	}
	c.opts.Log.Info("the status page is on", "addr", c.ln.Addr().String(), "network", cfg.Network,
		"https", cfg.Network || cfg.TLSCert != "", "token", cfg.Token != "")
}

// startLocked opens the listener and serves the page on it; c.mu is held.
func (c *Controller) startLocked(cfg Config) error {
	var tlsCfg *tls.Config
	switch {
	case cfg.TLSCert != "":
		cert, err := loadGiven(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return err
		}
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	case cfg.Network:
		host, _, _ := net.SplitHostPort(cfg.Listen)
		hostname, _ := c.opts.Hostname()
		cert, err := selfSigned(c.opts.DataDir, host, hostname, c.opts.Now())
		if err != nil {
			return err
		}
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	ln = netutil.LimitListener(ln, maxConns)
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
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
	c.srv, c.ln = srv, ln
	go func() { _ = srv.Serve(ln) }()
	return nil
}

// stopLocked closes the server, if any; c.mu is held.
func (c *Controller) stopLocked() {
	if c.srv != nil {
		_ = c.srv.Close()
		c.srv, c.ln = nil, nil
	}
}

// Mode is what the page does now.
func (c *Controller) Mode() Mode {
	c.mu.Lock()
	defer c.mu.Unlock()
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

// Close stops the page for good; a later Apply does nothing.
func (c *Controller) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopLocked()
	c.closed, c.mode = true, ModeOff
}
