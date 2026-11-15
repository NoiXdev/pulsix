package http

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// reservedHeaders are the headers the probe controls; a secret's header
// may not be one of them.
var reservedHeaders = map[string]bool{
	"Host": true, "Content-Length": true, "Transfer-Encoding": true, "Connection": true, "User-Agent": true,
}

// credentials is the header a check's secret sends; header "" sends none.
// It never prints its value.
type credentials struct {
	header, value string
}

func (credentials) String() string   { return "http credentials" }
func (credentials) GoString() string { return "http credentials" }

// apply sets the credential header on req.
func (c credentials) apply(req *http.Request) {
	if c.header != "" {
		req.Header.Set(c.header, c.value)
	}
}

// credentialsFor reads the fields of the secret named name: exactly one of
// username and password (Basic), token (Bearer), or header and value (a
// header of its own). Other fields are ignored. No name means no
// credentials. Errors name the secret and the field, never a value.
func credentialsFor(name string, fields map[string]string) (credentials, error) {
	if name == "" {
		return credentials{}, nil
	}
	has := func(f string) bool { return fields[f] != "" }
	basic := has("username") || has("password")
	bearer := has("token")
	custom := has("header") || has("value")
	switch {
	case basic && !bearer && !custom && has("username") && has("password"):
		if strings.Contains(fields["username"], ":") {
			return credentials{}, fieldError(name, "username", "must not contain a colon")
		}
		raw := fields["username"] + ":" + fields["password"]
		return credentials{header: "Authorization", value: "Basic " + base64.StdEncoding.EncodeToString([]byte(raw))}, nil
	case bearer && !basic && !custom:
		if !httpguts.ValidHeaderFieldValue(fields["token"]) {
			return credentials{}, fieldError(name, "token", "must be a valid HTTP header value")
		}
		return credentials{header: "Authorization", value: "Bearer " + fields["token"]}, nil
	case custom && !basic && !bearer && has("header") && has("value"):
		header := fields["header"]
		if !httpguts.ValidHeaderFieldName(header) {
			return credentials{}, fieldError(name, "header", "must be an HTTP header name")
		}
		header = http.CanonicalHeaderKey(header)
		if reservedHeaders[header] {
			return credentials{}, fieldError(name, "header", "must not be Host, Content-Length, Transfer-Encoding, Connection or User-Agent")
		}
		if !httpguts.ValidHeaderFieldValue(fields["value"]) {
			return credentials{}, fieldError(name, "value", "must be a valid HTTP header value")
		}
		return credentials{header: header, value: fields["value"]}, nil
	}
	return credentials{}, fmt.Errorf("secret %q must hold username and password, token, or header and value", name)
}

// fieldError names the secret and the field that is wrong with it, never a
// value.
func fieldError(secret, field, problem string) error {
	return fmt.Errorf("secret %q: field %q %s", secret, field, problem)
}
