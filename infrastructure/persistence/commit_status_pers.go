package persistence

import (
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type commitStatusPers struct{ db *gorm.DB }

func NewCommitStatusPers(db *gorm.DB) *commitStatusPers { return &commitStatusPers{db: db} }

func (p *commitStatusPers) Create(s *domain.CommitStatus) error {
	s.Id = utils.UUIDv4()
	return p.db.Omit("Creator").Create(s).Error
}

func (p *commitStatusPers) List(repositoryId, sha string, limit int) ([]domain.CommitStatus, error) {
	var out []domain.CommitStatus
	err := p.db.Preload("Creator").Where("repository_id = ? AND sha = ?", repositoryId, sha).
		Order("created_at DESC, id DESC").Limit(limit).Find(&out).Error
	return out, err
}

// maxLatestRows bounds Latest: commits with thousands of reports (a CI
// retrying in a loop) are read up to this many rows.
const maxLatestRows = 5000

func (p *commitStatusPers) Latest(repositoryId string, shas []string) (map[string][]domain.CommitStatus, error) {
	out := map[string][]domain.CommitStatus{}
	if len(shas) == 0 {
		return out, nil
	}
	var rows []domain.CommitStatus
	err := p.db.Preload("Creator").Where("repository_id = ? AND sha IN ?", repositoryId, shas).
		Order("created_at DESC, id DESC").Limit(maxLatestRows).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, s := range rows { // newest first: the first per context wins
		if k := s.SHA + "\x00" + s.Context; !seen[k] {
			seen[k] = true
			out[s.SHA] = append(out[s.SHA], s)
		}
	}
	return out, nil
}

func (p *commitStatusPers) Contexts(repositoryId string, since time.Time, limit int) ([]string, error) {
	var out []string
	err := p.db.Model(&domain.CommitStatus{}).Select("context").
		Where("repository_id = ? AND created_at >= ?", repositoryId, since).
		Group("context").Order("MAX(created_at) DESC").Limit(limit).Pluck("context", &out).Error
	return out, err
}
