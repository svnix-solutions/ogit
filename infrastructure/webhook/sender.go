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
	"github.com/labbs/ogit/infrastructure/netguard"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// ErrBlockedAddress: the URL resolves to a private, loopback or otherwise
// internal address that webhooks.allowed_hosts doesn't allow.
var ErrBlockedAddress = errors.New("webhook target is a private or local address; allow it with webhooks.allowed_hosts")

// Sender delivers webhook requests.
type Sender struct {
	client *http.Client
}

// NewSender allows private targets listed in allowed (hostnames, IPs or
// CIDRs).
func NewSender(allowed []string, timeout time.Duration) (*Sender, error) {
	guard, err := netguard.New(allowed, "webhooks.allowed_hosts")
	if err != nil {
		return nil, err
	}
	dial := guard.DialContext(timeout)
	transport := &http.Transport{
		Proxy: nil, // a proxy would hide the real target from the check
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dial(ctx, network, addr)
			if errors.Is(err, netguard.ErrBlockedAddress) {
				return nil, ErrBlockedAddress
			}
			return conn, err
		},
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          20,
		IdleConnTimeout:       30 * time.Second,
	}
	return &Sender{client: &http.Client{
		Transport: transport,
		Timeout:   timeout,
		// Like GitHub: a redirect is a failed delivery, not followed (it
		// could lead to an internal address).
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
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
