package checkplugins

// maxParamStringLen bounds every string parameter of a synthetic plugin:
// 500 bytes holds any URL, host name, banner or answer a probe reasonably
// checks for while keeping a definition from smuggling kilobytes into every
// probe. internal/probe trusts params that pass here, so the two must
// agree — ranges, enums and the {address} placeholder included.
const maxParamStringLen = 500

// syntheticTimeout is the timeout_s every synthetic plugin takes, in
// seconds.
func syntheticTimeout(maxSeconds, dflt int64) ParamSpec {
	return ParamSpec{Name: "timeout_s", Type: TypeInteger, Min: bound(1), Max: bound(maxSeconds), Default: dflt}
}

// syntheticPort is a TCP port; dflt is nil when the plugin has none.
func syntheticPort(required bool, dflt any) ParamSpec {
	return ParamSpec{Name: "port", Type: TypeInteger, Required: required, Min: bound(1), Max: bound(65535), Default: dflt}
}

// syntheticText is a string parameter of at most maxParamStringLen bytes;
// nonEmpty refuses "" (a substring to look for), the probe's send accepts it.
func syntheticText(name string, nonEmpty bool) ParamSpec {
	return ParamSpec{Name: name, Type: TypeString, NonEmpty: nonEmpty, MaxBytes: maxParamStringLen}
}
