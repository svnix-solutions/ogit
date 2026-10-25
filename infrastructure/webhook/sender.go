// Package webhook sends webhook requests: signed like GitHub's, with a
// timeout, no redirects, and no connections to private addresses unless
// allowed (a webhook URL must not let a repository maintainer reach the
// server's internal network).
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrBlockedAddress: the URL resolves to a private, loopback or otherwise
// internal address that webhooks.allowed_hosts doesn't allow.
var ErrBlockedAddress = errors.New("webhook target is a private or local address; allow it with webhooks.allowed_hosts")

// Sender delivers webhook requests.
type Sender struct {
	client  *http.Client
	allowed []*net.IPNet
	hosts   map[string]bool
}

// NewSender allows private targets listed in allowed (hostnames, IPs or
// CIDRs).
func NewSender(allowed []string, timeout time.Duration) (*Sender, error) {
	s := &Sender{hosts: map[string]bool{}}
	for _, a := range allowed {
		a = strings.TrimSpace(a)
		switch {
		case a == "":
		case strings.Contains(a, "/"):
			_, n, err := net.ParseCIDR(a)
			if err != nil {
				return nil, fmt.Errorf("webhooks.allowed_hosts: %q is not a valid CIDR", a)
			}
			s.allowed = append(s.allowed, n)
		case net.ParseIP(a) != nil:
			ip := net.ParseIP(a)
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			s.allowed = append(s.allowed, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		default:
			s.hosts[strings.ToLower(a)] = true
		}
	}
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		Proxy: nil, // a proxy would hide the real target from the check
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, _ := net.SplitHostPort(addr)
			d := *dialer
			// Check the address actually dialed, after DNS: a name can't
			// point the hook at an internal address (DNS rebinding included).
			d.Control = func(_, address string, _ syscall.RawConn) error {
				ipStr, _, _ := net.SplitHostPort(address)
				if !s.permitted(host, net.ParseIP(ipStr)) {
					return ErrBlockedAddress
				}
				return nil
			}
			return d.DialContext(ctx, network, addr)
		},
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          20,
		IdleConnTimeout:       30 * time.Second,
	}
	s.client = &http.Client{
		Transport: transport,
		Timeout:   timeout,
		// Like GitHub: a redirect is a failed delivery, not followed (it
		// could lead to an internal address).
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return s, nil
}

func (s *Sender) permitted(host string, ip net.IP) bool {
	if ip == nil {
		return false
	}
	if s.hosts[strings.ToLower(host)] {
		return true
	}
	for _, n := range s.allowed {
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

// ValidateURL checks a hook URL's form (http or https, a host). Addresses
// are checked when connecting.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return errors.New("webhook URL must be http(s)://host/path, without credentials")
	}
	return nil
}

// Request is one delivery attempt.
type Request struct {
	URL, Secret       string
	Event, DeliveryId string
	HookId            string
	Payload           []byte
}

// Response of an attempt; Err is set when no response came back.
type Response struct {
	Status   int
	Body     string // first 4 KiB
	Duration time.Duration
	Err      error
}

// Signature is GitHub's X-Hub-Signature-256 value.
func Signature(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (s *Sender) Send(ctx context.Context, r Request) Response {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(r.Payload))
	if err != nil {
		return Response{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "oGit-Hookshot/1")
	for _, prefix := range []string{"X-GitHub-", "X-Ogit-"} {
		req.Header.Set(prefix+"Event", r.Event)
		req.Header.Set(prefix+"Delivery", r.DeliveryId)
		req.Header.Set(prefix+"Hook-ID", r.HookId)
	}
	if r.Secret != "" {
		req.Header.Set("X-Hub-Signature-256", Signature(r.Secret, r.Payload))
		mac := hmac.New(sha1.New, []byte(r.Secret)) // older consumers read the SHA-1 header
		mac.Write(r.Payload)
		req.Header.Set("X-Hub-Signature", "sha1="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Response{Duration: time.Since(start), Err: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return Response{Status: resp.StatusCode, Body: string(body), Duration: time.Since(start)}
}
