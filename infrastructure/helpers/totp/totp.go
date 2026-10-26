// Package totp implements time-based one-time passwords (RFC 6238) as
// authenticator apps use them: HMAC-SHA1, 30-second steps, 6 digits.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	Period = 30 * time.Second
	Digits = 6
	// Skew is how many steps before and after now are accepted (clock
	// drift and typing time).
	Skew = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a random 160-bit secret, base32 (what apps expect).
func NewSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

// Step is the time step of t.
func Step(t time.Time) int64 { return t.Unix() / int64(Period/time.Second) }

// Code is the code of secret at step.
func Code(secret string, step int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil {
		return "", err
	}
	return code(key, step, Digits), nil
}

func code(key []byte, step int64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for range digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, n%mod)
}

// Validate checks code at now (within Skew steps) and returns the step it
// matched; the caller refuses steps already used (replays).
func Validate(secret, input string, now time.Time) (int64, bool) {
	input = strings.ReplaceAll(strings.TrimSpace(input), " ", "")
	if len(input) != Digits {
		return 0, false
	}
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil {
		return 0, false
	}
	now0 := Step(now)
	for d := int64(-Skew); d <= Skew; d++ {
		if subtle.ConstantTimeCompare([]byte(code(key, now0+d, Digits)), []byte(input)) == 1 {
			return now0 + d, true
		}
	}
	return 0, false
}

// URI is the otpauth:// URI apps scan (as a QR code).
func URI(issuer, account, secret string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {fmt.Sprint(Digits)}, "period": {"30"}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}
