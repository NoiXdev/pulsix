package checkplugins

// tlsPlugin: a TLS handshake and the certificate's expiry, from the Pulsix
// server (backlog 3.13). server_name defaults to the host's address and may
// contain {address}.
//
// Backlog 3.87 added assertions (withAssertions) on the handshake and the
// leaf certificate.
func tlsPlugin() Descriptor {
	return withAssertions(Descriptor{
		Name: "tls",
		Runs: Server,
		Params: []ParamSpec{
			syntheticPort(false, int64(443)),
			{Name: "server_name", Type: TypeString, MaxBytes: maxParamStringLen, Format: FormatNotBlank},
			syntheticTimeout(30, 10),
		},
		Metrics: []MetricSpec{
			{Name: "tls.up", Kind: Numeric},
			// The failure reason of an attempt (probe.Failure*): "none" while
			// up, else why it failed.
			{Name: "tls.failure", Kind: Discrete},
			{Name: "tls.handshake_ms", Kind: Numeric},
			{Name: "tls.cert_valid", Kind: Numeric},
			{Name: "tls.cert_expiry_days", Kind: Numeric},
			// The leaf's issuer: common name, else organization, else DN.
			{Name: "tls.cert_issuer", Kind: Discrete},
		},
	}, FormatTLSAssertion, tlsAssertionExample)
}

// tlsAssertionExample is a valid tls assertion.
const tlsAssertionExample = "modern=version >= 1.2"
