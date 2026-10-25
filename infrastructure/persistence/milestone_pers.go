package persistence

import (
	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type milestonePers struct{ db *gorm.DB }

func NewMilestonePers(db *gorm.DB) *milestonePers { return &milestonePers{db: db} }

func (p *milestonePers) List(repositoryId string, state domain.MilestoneState) ([]domain.Milestone, error) {
	q := p.db.Where("repository_id = ?", repositoryId)
	if state != "" {
		q = q.Where("state = ?", state)
	}
	var out []domain.Milestone
	err := q.Order("CASE WHEN due_on IS NULL THEN 1 ELSE 0 END, due_on, title").Find(&out).Error
	return out, err
}

func (p *milestonePers) Get(repositoryId, id string) (*domain.Milestone, error) {
	var m domain.Milestone
	if err := p.db.Where("repository_id = ? AND id = ?", repositoryId, id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (p *milestonePers) Create(m *domain.Milestone) error {
	m.Id = utils.UUIDv4()
	return p.db.Create(m).Error
}

func (p *milestonePers) Update(id string, fields map[string]any) error {
	return p.db.Model(&domain.Milestone{}).Where("id = ?", id).Updates(fields).Error
}

// Delete clears the milestone from issues and pull requests first (SQLite
// does not apply ON DELETE SET NULL here).
func (p *milestonePers) Delete(id string) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		for _, q := range []string{
			"UPDATE issue SET milestone_id = NULL WHERE milestone_id = ?",
			"UPDATE pull_request SET milestone_id = NULL WHERE milestone_id = ?",
			"DELETE FROM milestone WHERE id = ?",
		} {
			if err := tx.Exec(q, id).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (p *milestonePers) Progress(ids []string) (map[string]domain.MilestoneProgress, error) {
	out := map[string]domain.MilestoneProgress{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		MilestoneId string
		Open        bool
		N           int64
	}
	err := p.db.Raw(`
		SELECT milestone_id, state = 'open' AS open, COUNT(*) AS n FROM issue WHERE milestone_id IN ? GROUP BY milestone_id, state = 'open'
		UNION ALL
		SELECT milestone_id, state = 'open' AS open, COUNT(*) AS n FROM pull_request WHERE milestone_id IN ? GROUP BY milestone_id, state = 'open'`,
		ids, ids).Scan(&rows).Error
	for _, r := range rows {
		pr := out[r.MilestoneId]
		if r.Open {
			pr.Open += r.N
		} else {
			pr.Closed += r.N
		}
		out[r.MilestoneId] = pr
	}
	return out, err
}
