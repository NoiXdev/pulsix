package snmp

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/probe"
)

// Parameter bounds and defaults; internal/checkplugins/snmp.go validates
// the same ones (a test keeps them equal), so params that reach the probe
// pass here. parseSettings checks them again because a probe never trusts
// what it dials with.
const (
	defPort        = 161
	defTimeoutSecs = 5
	maxTimeoutSecs = 30
	maxOIDs        = 32
	// minPasswordLen is SNMPv3's minimum pass phrase length (RFC 3414
	// section 11.2); agents refuse shorter ones.
	minPasswordLen = 8
)

const errNoSecretParam = "the secret parameter is not set"

// oidEntry is one oids entry: the item it reports on and its OID with a
// leading dot, the form devices answer with. bad marks an OID the probe
// cannot ask for; only its own item fails.
type oidEntry struct {
	name, oid string
	bad       bool
}

// settings are the params of one snmp check.
type settings struct {
	secret     string
	version    string
	port       int
	timeout    time.Duration
	oids       []oidEntry
	interfaces bool
}

// parseSettings reads and checks params. Its errors quote no param value.
func parseSettings(params map[string]any) (settings, error) {
	s := settings{
		secret:     probe.StrParam(params, "secret", ""),
		version:    probe.StrParam(params, "version", "v2c"),
		interfaces: probe.BoolParam(params, "interfaces", false),
	}
	if s.secret == "" {
		return s, errors.New(errNoSecretParam)
	}
	if s.version != "v2c" && s.version != "v3" {
		return s, errors.New("unsupported version")
	}
	port, ok := probe.PortParam(params, defPort)
	if !ok {
		return s, probe.ErrBadPort
	}
	s.port = port
	secs := probe.NumParam(params, "timeout_seconds", defTimeoutSecs)
	if secs < 1 || secs > maxTimeoutSecs || secs != float64(int(secs)) {
		return s, errors.New("invalid timeout_seconds")
	}
	s.timeout = time.Duration(secs) * time.Second
	entries, ok := stringList(params["oids"])
	if !ok || len(entries) > maxOIDs {
		return s, errors.New("invalid oids")
	}
	for _, e := range entries {
		name, oid, ok := strings.Cut(e, "=")
		if !ok || !checkplugins.FormatSecretName.Valid(name) {
			return s, errors.New("invalid oids entry")
		}
		s.oids = append(s.oids, oidEntry{
			name: name,
			oid:  "." + strings.TrimPrefix(oid, "."),
			bad:  !checkplugins.FormatSNMPOID.Valid(e) || !encodable(oid),
		})
	}
	if len(s.oids) == 0 && !s.interfaces {
		return s, errors.New("nothing to poll")
	}
	return s, nil
}

// stringList reads a list param; JSON lists arrive as []any. An absent
// param is the empty list.
func stringList(v any) ([]string, bool) {
	switch l := v.(type) {
	case nil:
		return nil, true
	case []string:
		return l, true
	case []any:
		out := make([]string, 0, len(l))
		for _, e := range l {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

// credentials are what a device is asked with. They never format their
// values (String, GoString), so a stray %v cannot leak them.
type credentials struct {
	version   gosnmp.SnmpVersion
	community string
	flags     gosnmp.SnmpV3MsgFlags
	usm       *gosnmp.UsmSecurityParameters
}

func (credentials) String() string   { return "snmp credentials" }
func (credentials) GoString() string { return "snmp credentials" }

// fieldError names the secret and the field that is wrong with it, never a
// value.
func fieldError(secret, field, problem string) error {
	return fmt.Errorf("secret %q: field %q %s", secret, field, problem)
}

var (
	authProtocols = map[string]gosnmp.SnmpV3AuthProtocol{"SHA": gosnmp.SHA, "SHA-256": gosnmp.SHA256, "SHA-512": gosnmp.SHA512}
	// AES-256 is the Blumenthal key extension (net-snmp's AES-256 and
	// RFC draft-blumenthal-aes-usm); the Reeder variant some vendors
	// call AES-256-C is not offered.
	privProtocols = map[string]gosnmp.SnmpV3PrivProtocol{"AES": gosnmp.AES, "AES-256": gosnmp.AES256}
)

// credentialsFor reads the fields of the secret named name for version:
// v2c needs community; v3 needs username, auth_protocol and auth_password
// (authNoPriv), plus priv_protocol and priv_password for authPriv.
// noAuthNoPriv is refused.
func credentialsFor(name, version string, secret map[string]string) (credentials, error) {
	field := func(f string) (string, error) {
		v := secret[f]
		if v == "" {
			return "", fieldError(name, f, "is missing")
		}
		return v, nil
	}
	if version == "v2c" {
		c, err := field("community")
		if err != nil {
			return credentials{}, err
		}
		return credentials{version: gosnmp.Version2c, community: c}, nil
	}

	user, err := field("username")
	if err != nil {
		return credentials{}, err
	}
	authName, err := field("auth_protocol")
	if err != nil {
		return credentials{}, err
	}
	auth, ok := authProtocols[authName]
	if !ok {
		return credentials{}, fieldError(name, "auth_protocol", "must be SHA, SHA-256 or SHA-512")
	}
	authPass, err := field("auth_password")
	if err != nil {
		return credentials{}, err
	}
	if len(authPass) < minPasswordLen {
		return credentials{}, fieldError(name, "auth_password", "must be at least 8 characters")
	}
	usm := &gosnmp.UsmSecurityParameters{
		UserName:                 user,
		AuthenticationProtocol:   auth,
		AuthenticationPassphrase: authPass,
		PrivacyProtocol:          gosnmp.NoPriv,
	}
	c := credentials{version: gosnmp.Version3, flags: gosnmp.AuthNoPriv, usm: usm}

	privName, privPass := secret["priv_protocol"], secret["priv_password"]
	switch {
	case privName == "" && privPass == "":
		return c, nil
	case privName == "":
		return credentials{}, fieldError(name, "priv_protocol", "is missing")
	case privPass == "":
		return credentials{}, fieldError(name, "priv_password", "is missing")
	}
	priv, ok := privProtocols[privName]
	if !ok {
		return credentials{}, fieldError(name, "priv_protocol", "must be AES or AES-256")
	}
	if len(privPass) < minPasswordLen {
		return credentials{}, fieldError(name, "priv_password", "must be at least 8 characters")
	}
	usm.PrivacyProtocol, usm.PrivacyPassphrase = priv, privPass
	c.flags = gosnmp.AuthPriv
	return c, nil
}

// encodable reports whether gosnmp can put oid into a request. The format
// already refuses what X.690 cannot encode; this asks gosnmp's own encoder,
// so an OID that still slipped through fails its item alone instead of the
// whole request it would have been part of.
func encodable(oid string) bool {
	x := &gosnmp.GoSNMP{Version: gosnmp.Version2c}
	_, err := x.SnmpEncodePacket(gosnmp.GetRequest, []gosnmp.SnmpPDU{{Name: oid, Type: gosnmp.Null}}, 0, 0)
	return err == nil
}
