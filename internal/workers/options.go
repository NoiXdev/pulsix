package workers

import (
	"io"
	"time"
)

// Option configures a Service.
type Option func(*Service)

// WithSigner wires the CA for enrollment and renewal.
func WithSigner(s Signer) Option { return func(svc *Service) { svc.signer = s } }

// WithZones wires the zone lookup enrollment creation validates against.
func WithZones(z ZoneLister) Option { return func(svc *Service) { svc.zones = z } }

// WithClock injects the clock (tests).
func WithClock(now func() time.Time) Option { return func(svc *Service) { svc.now = now } }

// WithRand injects the entropy source for ids and enrollment secrets.
func WithRand(r io.Reader) Option { return func(svc *Service) { svc.rand = r } }
