// Package ldaptest is an in-process LDAP server for tests: simple binds
// and searches (equality, presence, and, or, not filters) over entries
// held in memory.
package ldaptest

import (
	"net"
	"strings"
	"sync"
	"testing"

	ber "github.com/go-asn1-ber/asn1-ber"
)

// Entry is a directory entry; Password is its userPassword ("" can't
// bind).
type Entry struct {
	DN       string
	Attrs    map[string][]string
	Password string
}

// Server serves entries on 127.0.0.1.
type Server struct {
	URL string
	// ServiceDN must bind before searching (when set).
	ServiceDN, ServicePassword string

	mu      sync.Mutex
	entries []Entry
	binds   []string // every bind attempt: "<dn>" or "<dn> (empty password)"
	ln      net.Listener
}

// Start serves entries until the test ends.
func Start(t *testing.T, entries ...Entry) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{URL: "ldap://" + ln.Addr().String(), entries: entries, ln: ln}
	go s.accept()
	t.Cleanup(func() { ln.Close() })
	return s
}

// SetEntries replaces the directory's content.
func (s *Server) SetEntries(entries ...Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = entries
}

// Binds lists the bind attempts so far.
func (s *Server) Binds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.binds...)
}

func (s *Server) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(conn)
	}
}

const (
	appBindRequest    = 0
	appBindResponse   = 1
	appUnbindRequest  = 2
	appSearchRequest  = 3
	appSearchEntry    = 4
	appSearchDone     = 5
	resultSuccess     = 0
	resultSizeLimit   = 4
	resultInvalidCred = 49
	resultInsufficent = 50
	resultUnwilling   = 53
)

func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	bound := ""
	for {
		packet, err := ber.ReadPacket(conn)
		if err != nil || len(packet.Children) < 2 {
			return
		}
		id := packet.Children[0].Value
		op := packet.Children[1]
		switch op.Tag {
		case appBindRequest:
			dn, password := "", ""
			if len(op.Children) >= 3 {
				dn, _ = op.Children[1].Value.(string)
				password = op.Children[2].Data.String()
			}
			code := s.bind(dn, password)
			if code == resultSuccess {
				bound = dn
			} else {
				bound = ""
			}
			s.send(conn, id, result(appBindResponse, code))
		case appUnbindRequest:
			return
		case appSearchRequest:
			if s.ServiceDN != "" && bound == "" {
				s.send(conn, id, result(appSearchDone, resultInsufficent))
				continue
			}
			s.search(conn, id, op)
		default:
			s.send(conn, id, result(op.Tag+1, resultUnwilling))
		}
	}
}

func (s *Server) bind(dn, password string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if password == "" {
		s.binds = append(s.binds, dn+" (empty password)")
		if dn == "" {
			return resultSuccess // anonymous
		}
		return resultUnwilling // unauthenticated binds refused
	}
	s.binds = append(s.binds, dn)
	if s.ServiceDN != "" && strings.EqualFold(dn, s.ServiceDN) {
		if password == s.ServicePassword {
			return resultSuccess
		}
		return resultInvalidCred
	}
	for _, e := range s.entries {
		if strings.EqualFold(e.DN, dn) && e.Password != "" && e.Password == password {
			return resultSuccess
		}
	}
	return resultInvalidCred
}

func (s *Server) search(conn net.Conn, id any, op *ber.Packet) {
	if len(op.Children) < 8 {
		s.send(conn, id, result(appSearchDone, resultUnwilling))
		return
	}
	base, _ := op.Children[0].Value.(string)
	limit, _ := op.Children[3].Value.(int64)
	filter := op.Children[6]
	var wanted []string
	for _, a := range op.Children[7].Children {
		if name, ok := a.Value.(string); ok {
			wanted = append(wanted, name)
		}
	}
	s.mu.Lock()
	entries := append([]Entry(nil), s.entries...)
	s.mu.Unlock()
	sent := 0
	for _, e := range entries {
		if !strings.HasSuffix(strings.ToLower(e.DN), strings.ToLower(base)) || !matches(filter, e) {
			continue
		}
		if limit > 0 && int64(sent) >= limit {
			s.send(conn, id, result(appSearchDone, resultSizeLimit))
			return
		}
		s.send(conn, id, entryPacket(e, wanted))
		sent++
	}
	s.send(conn, id, result(appSearchDone, resultSuccess))
}

// matches evaluates an LDAP filter packet against e.
func matches(f *ber.Packet, e Entry) bool {
	switch f.Tag {
	case 0: // and
		for _, c := range f.Children {
			if !matches(c, e) {
				return false
			}
		}
		return true
	case 1: // or
		for _, c := range f.Children {
			if matches(c, e) {
				return true
			}
		}
		return false
	case 2: // not
		return len(f.Children) == 1 && !matches(f.Children[0], e)
	case 3: // equalityMatch
		if len(f.Children) != 2 {
			return false
		}
		attr, want := f.Children[0].Data.String(), f.Children[1].Data.String()
		for _, v := range values(e, attr) {
			if strings.EqualFold(v, want) {
				return true
			}
		}
		return false
	case 7: // present
		return len(values(e, f.Data.String())) > 0
	}
	return false
}

func values(e Entry, attr string) []string {
	for k, v := range e.Attrs {
		if strings.EqualFold(k, attr) {
			return v
		}
	}
	return nil
}

func entryPacket(e Entry, wanted []string) *ber.Packet {
	p := ber.Encode(ber.ClassApplication, ber.TypeConstructed, appSearchEntry, nil, "Search Result Entry")
	p.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, e.DN, "DN"))
	attrs := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "Attributes")
	for name, vals := range e.Attrs {
		if len(wanted) > 0 && !contains(wanted, name) {
			continue
		}
		a := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "Attribute")
		a.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, name, "Type"))
		set := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSet, nil, "Values")
		for _, v := range vals {
			set.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, v, "Value"))
		}
		a.AppendChild(set)
		attrs.AppendChild(a)
	}
	p.AppendChild(attrs)
	return p
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func result(tag ber.Tag, code int) *ber.Packet {
	p := ber.Encode(ber.ClassApplication, ber.TypeConstructed, tag, nil, "Result")
	p.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagEnumerated, int64(code), "Result Code"))
	p.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "", "Matched DN"))
	p.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "", "Diagnostic"))
	return p
}

func (s *Server) send(conn net.Conn, id any, op *ber.Packet) {
	msg := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "LDAP Message")
	n, _ := id.(int64)
	msg.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, n, "Message ID"))
	msg.AppendChild(op)
	_, _ = conn.Write(msg.Bytes())
}
