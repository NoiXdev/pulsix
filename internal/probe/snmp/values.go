package snmp

import (
	"math"
	"strconv"

	"github.com/gosnmp/gosnmp"

	"github.com/noixdev/pulsix/internal/probe"
)

const (
	// oidSysUpTime is SNMPv2-MIB sysUpTime.0, in hundredths of a second.
	oidSysUpTime = ".1.3.6.1.2.1.1.3.0"
	// oidsPerRequest bounds one Get: small enough for every device's PDU
	// size, large enough that 32 oids plus sysUpTime take three requests.
	oidsPerRequest = 16
)

// Observation errors. None quotes anything the device or the secret
// said: a library error can carry the community (it formats whole
// packets), so every failure maps to one of these.
const (
	errNoSuchObject     = "the device has no such object"
	errBadOID           = "invalid OID: SNMP cannot encode it"
	errUnsupportedType  = "the device answered with a value type the probe does not read"
	errNoAnswer         = "no answer from the device"
	errMalformed        = "the device answered with a malformed response"
	errNoStatus         = "the device did not report the interface's status"
	errNoInterfaceTable = "the device's interface table could not be read"
)

// errorStatus is the error for a whole answer whose error-status is set;
// the status is gosnmp's name of a fixed enum.
func errorStatus(e gosnmp.SNMPError) string {
	return "the device answered with error " + e.String()
}

// answers checks a Get answer against the OIDs asked for: one variable per
// OID, in order. It returns them, or the error every OID of the request
// gets.
func answers(pkt *gosnmp.SnmpPacket, oids []string) ([]gosnmp.SnmpPDU, string) {
	if pkt == nil {
		return nil, errMalformed
	}
	if pkt.Error != gosnmp.NoError {
		return nil, errorStatus(pkt.Error)
	}
	if len(pkt.Variables) != len(oids) {
		return nil, errMalformed
	}
	for i, v := range pkt.Variables {
		if normalizeOID(v.Name) != oids[i] {
			return nil, errMalformed
		}
	}
	return pkt.Variables, ""
}

// normalizeOID gives an OID its leading dot.
func normalizeOID(oid string) string {
	if len(oid) > 0 && oid[0] == '.' {
		return oid
	}
	return "." + oid
}

// noSuchObject reports whether a variable says the device does not have
// the object.
func noSuchObject(t gosnmp.Asn1BER) bool {
	return t == gosnmp.NoSuchObject || t == gosnmp.NoSuchInstance || t == gosnmp.EndOfMibView
}

// value is what one oids entry read: a number, a text (as the device sent
// it) or the error its snmp.value reports. A number also keeps spelled,
// its exact decimal spelling: a Counter64 above 2^53 has no exact float64,
// and the assertions compare text and show actuals by spelled.
type value struct {
	num     float64
	spelled string
	text    string
	isText  bool
	err     string
}

// readValue reads the answer for an oids entry: a number, a string, an OID
// or an IP address as text, the error otherwise.
func readValue(pdu gosnmp.SnmpPDU) value {
	switch pdu.Type {
	case gosnmp.Integer, gosnmp.Counter32, gosnmp.Counter64, gosnmp.Gauge32, gosnmp.TimeTicks, gosnmp.Uinteger32:
		if v, ok := toFloat(pdu.Value); ok {
			return value{num: v, spelled: spell(pdu.Value, v)}
		}
	case gosnmp.OpaqueFloat, gosnmp.OpaqueDouble:
		if v, ok := toFloat(pdu.Value); ok && !math.IsNaN(v) && !math.IsInf(v, 0) {
			return value{num: v, spelled: spell(pdu.Value, v)}
		}
	case gosnmp.OctetString, gosnmp.ObjectIdentifier, gosnmp.IPAddress:
		if s, ok := toText(pdu.Value); ok {
			return value{text: s, isText: true}
		}
	default:
		if noSuchObject(pdu.Type) {
			return value{err: errNoSuchObject}
		}
	}
	return value{err: errUnsupportedType}
}

// observation is v as the oids entry name reports it: snmp.value for a
// number, snmp.text for a text, an error on snmp.value.
func (v value) observation(name string) probe.Observation {
	obs := probe.Observation{Metric: "snmp.value", Item: name}
	switch {
	case v.err != "":
		obs.Err = v.err
	case v.isText:
		l := probe.Label(v.text)
		obs.Metric, obs.Label = "snmp.text", &l
	default:
		n := v.num
		obs.Value = &n
	}
	return obs
}

// toFloat reads the Go value gosnmp decodes a number into.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		// Through its shortest float32 spelling, so a device's 0.15 is
		// 0.15 and not 0.15000000596046448 (review M1).
		f, err := strconv.ParseFloat(strconv.FormatFloat(float64(n), 'g', -1, 32), 64)
		return f, err == nil
	case float64:
		return n, true
	}
	return 0, false
}

// spell is the decimal spelling of the number v that toFloat read as f:
// an integer's own digits (exact past 2^53), a float's shortest form
// without an exponent.
func spell(v any, f float64) string {
	switch n := v.(type) {
	case int:
		return strconv.FormatInt(int64(n), 10)
	case int32:
		return strconv.FormatInt(int64(n), 10)
	case int64:
		return strconv.FormatInt(n, 10)
	case uint:
		return strconv.FormatUint(uint64(n), 10)
	case uint32:
		return strconv.FormatUint(uint64(n), 10)
	case uint64:
		return strconv.FormatUint(n, 10)
	}
	if f == 0 {
		f = 0 // no "-0"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// toUint reads an unsigned number (a counter, TimeTicks).
func toUint(v any) (uint64, bool) {
	switch n := v.(type) {
	case uint:
		return uint64(n), true
	case uint32:
		return uint64(n), true
	case uint64:
		return n, true
	case int:
		if n >= 0 {
			return uint64(n), true
		}
	}
	return 0, false
}

// toInt reads an INTEGER.
func toInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}

// toText reads a string value: OctetString arrives as bytes, an OID and an
// IP address as strings.
func toText(v any) (string, bool) {
	switch s := v.(type) {
	case []byte:
		return string(s), true
	case string:
		return s, true
	}
	return "", false
}
