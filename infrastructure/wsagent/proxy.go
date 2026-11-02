package wsagent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"time"

	"github.com/labbs/ogit/application"
)

// Headers of the server's requests. Everything else belongs to the browser.
const (
	HeaderProxyAuth = "X-Ogit-Proxy-Auth"
	HeaderWorkspace = "X-Ogit-Workspace"
	HeaderPort      = "X-Ogit-Port"
)

// proxyMaxAge bounds a request's signature, clock differences included.
const proxyMaxAge = 5 * time.Minute

// newCertificate makes the listener's certificate. It's new every run: the
// server pins the fingerprint reported in syncs, so no CA is involved.
func newCertificate() (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "ogit workspace agent"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	sum := sha256.Sum256(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, hex.EncodeToString(sum[:]), nil
}

// ProxyHandler passes the server's signed requests to workspaces. Only the
// server gets through, so the check of who may use a workspace stays there.
func (a *Agent) ProxyHandler() http.Handler {
	proxy := &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = r.In.Context().Value(proxyTarget{}).(string)
			r.Out.Host = r.In.Host
			for _, h := range []string{"X-Forwarded-Proto", "X-Forwarded-Host", "X-Forwarded-For"} {
				if v := r.In.Header.Get(h); v != "" {
					r.Out.Header.Set(h, v)
				}
			}
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "Nothing is listening on that port of the workspace yet.", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		key := a.key
		a.mu.Unlock()
		id := r.Header.Get(HeaderWorkspace)
		port, err := strconv.Atoi(r.Header.Get(HeaderPort))
		if key == "" || err != nil || port < 0 || port > 65535 ||
			!application.VerifyProxyRequest(key, r.Header.Get(HeaderProxyAuth), id, port, time.Now(), proxyMaxAge) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		for _, h := range []string{HeaderProxyAuth, HeaderWorkspace, HeaderPort} {
			r.Header.Del(h)
		}
		host, err := a.Runtime.Address(r.Context(), id)
		if err != nil {
			http.Error(w, "This workspace isn't running.", http.StatusBadGateway)
			return
		}
		if port == 0 {
			port = a.Runtime.EditorPort()
		}
		target := net.JoinHostPort(host, strconv.Itoa(port))
		proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), proxyTarget{}, target)))
	})
}

type proxyTarget struct{}

// ServeProxy listens for the server until ctx ends, and reports url to it.
func (a *Agent) ServeProxy(ctx context.Context, address, url string) error {
	certificate, fingerprint, err := newCertificate()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	a.ProxyURL, a.ProxyFingerprint = url, fingerprint
	server := &http.Server{Handler: a.ProxyHandler(), ReadHeaderTimeout: 30 * time.Second,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}}
	go func() {
		if err := server.ServeTLS(listener, "", ""); !errors.Is(err, http.ErrServerClosed) {
			a.logf("the proxy listener failed: %v", err)
		}
	}()
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	a.logf("Listening for the server on %s", address)
	return nil
}
