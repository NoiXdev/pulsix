package checkplugins

// tlsPlugin: a TLS handshake and the certificate's expiry, from the Pulsix
// server (backlog 3.13). server_name defaults to the host's address and may
// contain {address}.
func tlsPlugin() Descriptor {
	return Descriptor{
		Name: "tls",
		Runs: Server,
		Params: []ParamSpec{
			syntheticPort(false, int64(443)),
			{Name: "server_name", Type: TypeString, MaxBytes: maxParamStringLen, Format: FormatNotBlank},
			syntheticTimeout(30, 10),
		},
		Metrics: []MetricSpec{
			{Name: "tls.up", Kind: Numeric},
			{Name: "tls.handshake_ms", Kind: Numeric},
			{Name: "tls.cert_valid", Kind: Numeric},
			{Name: "tls.cert_expiry_days", Kind: Numeric},
		},
	}
}
