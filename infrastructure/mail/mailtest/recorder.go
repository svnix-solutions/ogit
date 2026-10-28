// Package mailtest provides mail senders for tests: a Recorder that keeps
// messages in memory, and Mailpit (mailpit.go, integration tag), a real
// SMTP server in a container.
package mailtest

import (
	"errors"
	"sync"
)

type Message struct {
	To, Subject, Body string
}

// Recorder implements mail.Sender by recording messages.
type Recorder struct {
	mu       sync.Mutex
	messages []Message
	fail     bool
}

// ErrSendFailed is returned by Send while failing is enabled.
var ErrSendFailed = errors.New("mailtest: send failed")

func (r *Recorder) Send(to, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return ErrSendFailed
	}
	r.messages = append(r.messages, Message{To: to, Subject: subject, Body: body})
	return nil
}

// Fail makes subsequent sends fail (true) or succeed (false).
func (r *Recorder) Fail(fail bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fail = fail
}

// Messages returns the messages sent to address, oldest first.
func (r *Recorder) Messages(address string) []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Message
	for _, m := range r.messages {
		if m.To == address {
			out = append(out, m)
		}
	}
	return out
}
