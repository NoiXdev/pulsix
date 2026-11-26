package hosts

import (
	"fmt"
	"net"
	"regexp"
)

// addressNamePattern is a DNS host name: the jump host address rule
// (sshconf's hostnamePattern), so neither accepts what the other refuses.
var addressNamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

// validateAddress refuses an address that is neither empty, an IP address
// nor a host name (decision D7, controller's call). The inventory hands it
// to Ansible as ansible_host, a connection variable Ansible templates, so
// a template, a lookup or an option-like value never gets that far,
// whoever writes it. Stored addresses are not rewritten; the renderer
// emits every address as plain text all the same.
func validateAddress(addr string) error {
	if addr == "" || net.ParseIP(addr) != nil || addressNamePattern.MatchString(addr) {
		return nil
	}
	return fmt.Errorf("%w: address must be an IP address or a host name", ErrInvalidInput)
}
