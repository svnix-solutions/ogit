package git

import (
	"errors"
	"io"
	"os"

	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
)

// ErrPushTooLarge: the pushed pack is over git.max_push_size.
var ErrPushTooLarge = errors.New("push is larger than the limit (git.max_push_size)")

// spooled is a pack staged in a temporary file, removed when closed.
type spooled struct{ *os.File }

func (s spooled) Close() error {
	err := s.File.Close()
	_ = os.Remove(s.Name())
	return err
}

// SpoolPack reads a push's pack into a temporary file, at most max bytes
// (0: no limit), and makes req read it from there. go-git writes the pack
// to storage as it reads it and closes its writer even on error, so a
// pack cut short (over the limit, or the client gone) would otherwise
// leave a partial pack behind. Over the limit it returns ErrPushTooLarge
// and req is untouched. The caller closes req.Packfile (go-git's
// ReceivePack does).
func SpoolPack(req *packp.ReferenceUpdateRequest, max int64) error {
	if req.Packfile == nil {
		return nil
	}
	f, err := os.CreateTemp("", "ogit-push-*.pack")
	if err != nil {
		return err
	}
	src := io.Reader(req.Packfile)
	if max > 0 {
		src = io.LimitReader(req.Packfile, max+1)
	}
	n, err := io.Copy(f, src)
	if err == nil && max > 0 && n > max {
		err = ErrPushTooLarge
	}
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	req.Packfile = spooled{f}
	return nil
}

// RefusedReport is the report for a push refused before its pack was read:
// the unpack status says why, every command is rejected.
func RefusedReport(req *packp.ReferenceUpdateRequest, reason error) *packp.ReportStatus {
	rs := packp.NewReportStatus()
	rs.UnpackStatus = reason.Error()
	for _, cmd := range req.Commands {
		rs.CommandStatuses = append(rs.CommandStatuses, &packp.CommandStatus{ReferenceName: cmd.Name, Status: reason.Error()})
	}
	return rs
}
