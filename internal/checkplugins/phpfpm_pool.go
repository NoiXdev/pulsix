package checkplugins

import (
	"errors"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// A phpfpm pool entry (backlog 3.11a) is either the pool's status page URL
// (FormatStatusURL) or the pool's own FastCGI address with its status
// path, read with no web server in between:
//
//	fcgi://HOST:PORT/PATH        TCP: an IP literal (IPv6 in brackets) or a
//	                             DNS name, a port 1–65535
//	unix:/SOCKET:/PATH           a unix socket: an absolute path
//
// The agent's phpfpm plugin and its local-check validation parse entries
// with the same functions, so the grammar has one home.
const (
	phpfpmTCPPrefix  = "fcgi://"
	phpfpmUnixPrefix = "unix:"
	// maxPHPFPMSocketLen is Linux's sun_path without its NUL.
	maxPHPFPMSocketLen = 107
)

// phpfpmStatusPath is the status path: '/' and at most 254 more bytes of
// letters, digits, '_', '~', '/' and '-'. No dot, ever: FPM runs
// SCRIPT_FILENAME as a script when the path is not the pool's
// pm.status_path, and a dotless path can never name a .php or .phar file
// (nor a x.php/… PATH_INFO split), so FPM's default
// security.limit_extensions refuses it instead of running PHP. No '?',
// '#', '%' or ':' either: the agent always asks for json itself.
var phpfpmStatusPath = regexp.MustCompile(`^/[A-Za-z0-9_~/-]{0,254}$`)

// phpfpmPort is a decimal port without a leading zero.
var phpfpmPort = regexp.MustCompile(`^[1-9][0-9]{0,4}$`)

// PHPFPMFastCGI is a parsed FastCGI pool entry.
type PHPFPMFastCGI struct {
	// Network is "tcp" or "unix", Address the host:port or the socket path
	// as net.Dial takes them.
	Network, Address string
	// Path is the status path, sent as SCRIPT_NAME and SCRIPT_FILENAME.
	Path string
}

// IsPHPFPMFastCGI reports whether a phpfpm pool entry is in one of the
// FastCGI forms (valid or not) rather than a status URL.
func IsPHPFPMFastCGI(s string) bool {
	return strings.HasPrefix(s, phpfpmTCPPrefix) || strings.HasPrefix(s, phpfpmUnixPrefix)
}

// Errors of ParsePHPFPMFastCGI, phrased to follow the parameter's name.
var (
	errPHPFPMPath   = errors.New("must end in a status path: / followed by at most 254 letters, digits, _ ~ / or - (no dot)")
	errPHPFPMTCP    = errors.New("must be fcgi://host:port/path, the host an IP address (IPv6 in brackets) or a DNS name and the port 1 to 65535")
	errPHPFPMSocket = errors.New("must be unix:/socket:/path, the socket an absolute path of at most 107 bytes without ':' or control characters")
)

// ParsePHPFPMFastCGI parses a FastCGI pool entry; the error says what is
// wrong with it.
func ParsePHPFPMFastCGI(s string) (PHPFPMFastCGI, error) {
	if rest, ok := strings.CutPrefix(s, phpfpmTCPPrefix); ok {
		return parsePHPFPMTCP(rest)
	}
	if rest, ok := strings.CutPrefix(s, phpfpmUnixPrefix); ok {
		return parsePHPFPMUnix(rest)
	}
	return PHPFPMFastCGI{}, errPHPFPMTCP
}

func parsePHPFPMTCP(rest string) (PHPFPMFastCGI, error) {
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return PHPFPMFastCGI{}, errPHPFPMPath
	}
	hostPort, path := rest[:slash], rest[slash:]
	colon := strings.LastIndexByte(hostPort, ':')
	if colon < 0 || !validPHPFPMHost(hostPort[:colon]) || !validPHPFPMPort(hostPort[colon+1:]) {
		return PHPFPMFastCGI{}, errPHPFPMTCP
	}
	if !phpfpmStatusPath.MatchString(path) {
		return PHPFPMFastCGI{}, errPHPFPMPath
	}
	return PHPFPMFastCGI{Network: "tcp", Address: hostPort, Path: path}, nil
}

// validPHPFPMHost: an IPv4 address, an IPv6 address in brackets (no zone)
// or a DNS name.
func validPHPFPMHost(h string) bool {
	if inner, ok := strings.CutPrefix(h, "["); ok {
		inner, closed := strings.CutSuffix(inner, "]")
		ip, err := netip.ParseAddr(inner)
		return closed && err == nil && ip.Is6() && ip.Zone() == ""
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		return ip.Is4()
	}
	return validDNSName(h)
}

func validPHPFPMPort(p string) bool {
	if !phpfpmPort.MatchString(p) {
		return false
	}
	n, err := strconv.Atoi(p)
	return err == nil && n <= 65535
}

func parsePHPFPMUnix(rest string) (PHPFPMFastCGI, error) {
	socket, path, ok := strings.Cut(rest, ":")
	if !strings.HasPrefix(socket, "/") || strings.HasPrefix(socket, "//") || len(socket) > maxPHPFPMSocketLen || hasControl(socket) {
		return PHPFPMFastCGI{}, errPHPFPMSocket
	}
	if !ok || !phpfpmStatusPath.MatchString(path) {
		return PHPFPMFastCGI{}, errPHPFPMPath
	}
	return PHPFPMFastCGI{Network: "unix", Address: socket, Path: path}, nil
}

// phpfpmPoolProblem is FormatPHPFPMPool: a FastCGI entry is held to its
// own grammar, anything else is a status URL.
func phpfpmPoolProblem(s string) string {
	if IsPHPFPMFastCGI(s) {
		if _, err := ParsePHPFPMFastCGI(s); err != nil {
			return err.Error()
		}
		return ""
	}
	if !validStatusURL(s) {
		return "must be an http:// or https:// URL with a host, no user info and no fragment (it must not contain {address}), or a FastCGI address fcgi://host:port/path or unix:/socket:/path"
	}
	return ""
}
