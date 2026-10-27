// Package netguard keeps outgoing connections that users configure
// (webhooks, repository mirrors) away from the server's internal network:
// private, loopback, link-local and other internal addresses are refused
// unless allowed.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"
)

// ErrBlockedAddress: the target resolves to an internal address that isn't
// allowed.
var ErrBlockedAddress = errors.New("target is a private or local address")

// Guard decides which addresses outgoing connections may reach.
type Guard struct {
	allowed []*net.IPNet
	hosts   map[string]bool
	// Resolve looks host names up (net.DefaultResolver; tests replace it).
	Resolve func(ctx context.Context, host string) ([]net.IP, error)
}

// New allows internal targets listed in allowed (host names, IPs or
// CIDRs); setting names the configuration key, for errors.
func New(allowed []string, setting string) (*Guard, error) {
	g := &Guard{hosts: map[string]bool{}}
	for _, a := range allowed {
		a = strings.TrimSpace(a)
		switch {
		case a == "":
		case strings.Contains(a, "/"):
			_, n, err := net.ParseCIDR(a)
			if err != nil {
				return nil, fmt.Errorf("%s: %q is not a valid CIDR", setting, a)
			}
			g.allowed = append(g.allowed, n)
		case net.ParseIP(a) != nil:
			ip := net.ParseIP(a)
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			g.allowed = append(g.allowed, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		default:
			g.hosts[strings.ToLower(a)] = true
		}
	}
	return g, nil
}

// Permitted reports whether host (as the user gave it) may be reached at
// ip.
func (g *Guard) Permitted(host string, ip net.IP) bool {
	if ip == nil {
		return false
	}
	if g.hosts[strings.ToLower(host)] {
		return true
	}
	for _, n := range g.allowed {
		if n.Contains(ip) {
			return true
		}
	}
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() || isSharedOrReserved(ip))
}

// isSharedOrReserved covers ranges net.IP's methods don't: carrier-grade
// NAT (100.64/10), 0/8, 192.0.0/24 and benchmarking (198.18/15).
func isSharedOrReserved(ip net.IP) bool {
	for _, cidr := range []string{"100.64.0.0/10", "0.0.0.0/8", "192.0.0.0/24", "198.18.0.0/15"} {
		_, n, _ := net.ParseCIDR(cidr)
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// DialContext dials like net.Dialer, checking the address actually dialed
// (after DNS, so a name can't be pointed at an internal address, DNS
// rebinding included).
func (g *Guard) DialContext(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: timeout}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(addr)
		d := *dialer
		d.Control = func(_, address string, _ syscall.RawConn) error {
			ipStr, _, _ := net.SplitHostPort(address)
			if !g.Permitted(host, net.ParseIP(ipStr)) {
				return ErrBlockedAddress
			}
			return nil
		}
		return d.DialContext(ctx, network, addr)
	}
}

// ResolveIP resolves host to an address it may reach, for clients that
// can't take a dialer (SSH through go-git): they dial the IP itself, so the
// name can't resolve elsewhere in between.
func (g *Guard) ResolveIP(ctx context.Context, host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if !g.Permitted(host, ip) {
			return nil, ErrBlockedAddress
		}
		return ip, nil
	}
	resolve := g.Resolve
	if resolve == nil {
		resolve = func(ctx context.Context, host string) ([]net.IP, error) {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			ips := make([]net.IP, 0, len(addrs))
			for _, a := range addrs {
				ips = append(ips, a.IP)
			}
			return ips, err
		}
	}
	ips, err := resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if g.Permitted(host, ip) {
			return ip, nil
		}
	}
	return nil, ErrBlockedAddress
}
