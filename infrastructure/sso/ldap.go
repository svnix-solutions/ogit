package sso

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
	"github.com/labbs/ogit/domain"
)

var (
	// ErrLDAPInvalidCredentials: no such user in the directory, or a
	// wrong password (the same answer for both).
	ErrLDAPInvalidCredentials = errors.New("invalid directory credentials")
	ErrLDAPConfig             = errors.New("invalid LDAP settings")
)

const ldapTimeout = 10 * time.Second

// LDAP is a directory to sign in with.
type LDAP struct {
	URL          string // ldap:// or ldaps://
	BindDN       string // the service account ("": anonymous search)
	BindPassword string
	Settings     domain.LDAPSettings
}

// WithDefaults fills the usual attributes and filters.
func WithDefaults(s domain.LDAPSettings) domain.LDAPSettings {
	def := func(v *string, d string) {
		if strings.TrimSpace(*v) == "" {
			*v = d
		}
	}
	def(&s.UsernameAttribute, "uid")
	def(&s.EmailAttribute, "mail")
	def(&s.NameAttribute, "cn")
	def(&s.UserFilter, "(&(objectClass=person)(|("+s.UsernameAttribute+"={input})("+s.EmailAttribute+"={input})))")
	def(&s.GroupNameAttribute, "cn")
	def(&s.GroupFilter, "(|(member={dn})(uniqueMember={dn})(memberUid={username}))")
	return s
}

// CheckLDAP checks a directory's URL and settings.
func CheckLDAP(raw string, s domain.LDAPSettings) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "ldap" && u.Scheme != "ldaps") || u.Host == "" {
		return fmt.Errorf("%w: the URL must be ldap://host or ldaps://host", ErrLDAPConfig)
	}
	if strings.TrimSpace(s.UserBase) == "" {
		return fmt.Errorf("%w: user_base is required", ErrLDAPConfig)
	}
	if s.UserFilter != "" && !strings.Contains(s.UserFilter, "{input}") {
		return fmt.Errorf("%w: user_filter must contain {input}", ErrLDAPConfig)
	}
	if s.UserFilter != "" {
		if _, err := ldap.CompileFilter(strings.ReplaceAll(s.UserFilter, "{input}", "x")); err != nil {
			return fmt.Errorf("%w: user_filter: %v", ErrLDAPConfig, err)
		}
	}
	if s.RootCA != "" && !x509.NewCertPool().AppendCertsFromPEM([]byte(s.RootCA)) {
		return fmt.Errorf("%w: root_ca holds no PEM certificate", ErrLDAPConfig)
	}
	return nil
}

func (d LDAP) tlsConfig() (*tls.Config, error) {
	u, _ := url.Parse(d.URL)
	cfg := &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12, InsecureSkipVerify: d.Settings.InsecureSkipVerify}
	if d.Settings.RootCA != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(d.Settings.RootCA)) {
			return nil, ErrLDAPConfig
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

func (d LDAP) connect(ctx context.Context) (*ldap.Conn, error) {
	tlsCfg, err := d.tlsConfig()
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: ldapTimeout}
	if deadline, ok := ctx.Deadline(); ok {
		dialer.Deadline = deadline
	}
	conn, err := ldap.DialURL(d.URL, ldap.DialWithDialer(dialer), ldap.DialWithTLSConfig(tlsCfg))
	if err != nil {
		return nil, fmt.Errorf("connecting to the directory: %w", err)
	}
	conn.SetTimeout(ldapTimeout)
	if d.Settings.StartTLS && strings.HasPrefix(d.URL, "ldap://") {
		if err := conn.StartTLS(tlsCfg); err != nil {
			conn.Close()
			return nil, fmt.Errorf("StartTLS: %w", err)
		}
	}
	return conn, nil
}

func (d LDAP) bindService(conn *ldap.Conn) error {
	if d.BindDN == "" {
		return nil // anonymous search
	}
	if err := conn.Bind(d.BindDN, d.BindPassword); err != nil {
		return fmt.Errorf("binding as the service account: %w", err)
	}
	return nil
}

// Authenticate finds input (a user name or email) in the directory and
// checks password with a bind as that user. ErrLDAPInvalidCredentials
// covers unknown users, several matches and wrong passwords.
func (d LDAP) Authenticate(ctx context.Context, input, password string) (*Claims, error) {
	input = strings.TrimSpace(input)
	// An empty password would be an unauthenticated bind, which many
	// servers accept.
	if input == "" || password == "" {
		return nil, ErrLDAPInvalidCredentials
	}
	s := WithDefaults(d.Settings)
	conn, err := d.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := d.bindService(conn); err != nil {
		return nil, err
	}
	attrs := []string{s.UsernameAttribute, s.EmailAttribute, s.NameAttribute}
	if s.SubjectAttribute != "" {
		attrs = append(attrs, s.SubjectAttribute)
	}
	if s.MemberOfAttribute != "" {
		attrs = append(attrs, s.MemberOfAttribute)
	}
	res, err := conn.Search(ldap.NewSearchRequest(s.UserBase, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, int(ldapTimeout.Seconds()), false,
		strings.ReplaceAll(s.UserFilter, "{input}", ldap.EscapeFilter(input)), attrs, nil))
	if err != nil && !ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded) {
		return nil, fmt.Errorf("searching the directory: %w", err)
	}
	if res == nil || len(res.Entries) != 1 {
		return nil, ErrLDAPInvalidCredentials
	}
	entry := res.Entries[0]
	if err := conn.Bind(entry.DN, password); err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			return nil, ErrLDAPInvalidCredentials
		}
		return nil, fmt.Errorf("checking the password: %w", err)
	}

	claims := &Claims{Subject: strings.ToLower(entry.DN), Username: entry.GetAttributeValue(s.UsernameAttribute),
		Email: entry.GetAttributeValue(s.EmailAttribute), Name: entry.GetAttributeValue(s.NameAttribute)}
	claims.EmailVerified = claims.Email != "" // the directory's own
	if s.SubjectAttribute != "" {
		if raw := entry.GetRawAttributeValue(s.SubjectAttribute); len(raw) > 0 {
			claims.Subject = s.SubjectAttribute + ":" + subjectValue(raw)
		}
	}
	switch {
	case s.MemberOfAttribute != "":
		for _, dn := range entry.GetAttributeValues(s.MemberOfAttribute) {
			if name := firstRDN(dn); name != "" {
				claims.Groups = append(claims.Groups, name)
			}
		}
	case s.GroupBase != "":
		if err := d.bindService(conn); err != nil {
			return nil, err
		}
		filter := strings.NewReplacer("{dn}", ldap.EscapeFilter(entry.DN), "{username}", ldap.EscapeFilter(claims.Username)).Replace(s.GroupFilter)
		groups, err := conn.Search(ldap.NewSearchRequest(s.GroupBase, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 1000, int(ldapTimeout.Seconds()), false,
			filter, []string{s.GroupNameAttribute}, nil))
		if err != nil {
			return nil, fmt.Errorf("searching groups: %w", err)
		}
		for _, g := range groups.Entries {
			if name := g.GetAttributeValue(s.GroupNameAttribute); name != "" {
				claims.Groups = append(claims.Groups, name)
			}
		}
	}
	return claims, nil
}

// subjectValue: text ids as they are, binary ones (objectGUID) in hex.
func subjectValue(raw []byte) string {
	for _, b := range raw {
		if b < 0x20 || b > 0x7e {
			return hex.EncodeToString(raw)
		}
	}
	return string(raw)
}

// firstRDN is the value of a DN's first component (cn=eng,ou=... → eng).
func firstRDN(dn string) string {
	parsed, err := ldap.ParseDN(dn)
	if err != nil || len(parsed.RDNs) == 0 || len(parsed.RDNs[0].Attributes) == 0 {
		return ""
	}
	return parsed.RDNs[0].Attributes[0].Value
}
