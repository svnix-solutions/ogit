package persistence

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type importPers struct{ db *gorm.DB }

func NewImportPers(db *gorm.DB) *importPers { return &importPers{db: db} }

func (p *importPers) Create(i *domain.Import) error {
	i.Id = utils.UUIDv4()
	return p.db.Create(i).Error
}

func (p *importPers) ForRepository(repositoryId string) (*domain.Import, error) {
	var i domain.Import
	if err := p.db.Where("repository_id = ?", repositoryId).First(&i).Error; err != nil {
		return nil, err
	}
	return &i, nil
}

func (p *importPers) Update(id string, fields map[string]any) error {
	return p.db.Model(&domain.Import{}).Where("id = ?", id).Updates(fields).Error
}

func (p *importPers) Delete(id string) error {
	return p.db.Where("id = ?", id).Delete(&domain.Import{}).Error
}

func (p *importPers) Ready(repositoryId string, now time.Time) error {
	return p.db.Model(&domain.Import{}).Where("repository_id = ? AND status = ?", repositoryId, domain.ImportWaiting).
		Updates(map[string]any{"status": domain.ImportPending, "next_run_at": now}).Error
}

// due: pending, or running with a lock that expired (a server stopped).
const importDue = "next_run_at <= ? AND (status = 'pending' OR (status = 'running' AND (locked_until IS NULL OR locked_until < ?)))"

func (p *importPers) Claim(now, lockUntil time.Time) (*domain.Import, error) {
	for range 5 {
		var i domain.Import
		err := p.db.Where(importDue, now, now).Order("next_run_at").First(&i).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		res := p.db.Model(&domain.Import{}).Where("id = ? AND "+importDue, i.Id, now, now).
			Updates(map[string]any{"locked_until": lockUntil, "status": domain.ImportRunning})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 1 {
			i.LockedUntil, i.Status = &lockUntil, domain.ImportRunning
			return &i, nil
		}
	}
	return nil, nil
}

func (p *importPers) Reserve(repositoryId string, n int) error {
	return p.db.Exec("UPDATE repository SET last_number = ? WHERE id = ? AND last_number < ?", n, repositoryId, n).Error
}

// number takes n for an imported item in table, or the next free one
// (0); false if an item of the other kind has it.
func number(tx *gorm.DB, table, repositoryId string, n *int) (bool, error) {
	if *n == 0 {
		next, err := nextNumber(tx, repositoryId)
		*n = next
		return true, err
	}
	// Issues and pull requests share numbers (the unique index covers
	// the item's own table).
	var count int64
	other := "pull_request"
	if table == "pull_request" {
		other = "issue"
	}
	if err := tx.Table(other).Where("repository_id = ? AND number = ?", repositoryId, *n).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}
	return true, tx.Exec("UPDATE repository SET last_number = ? WHERE id = ? AND last_number < ?", *n, repositoryId, *n).Error
}

func (p *importPers) AddIssue(issue *domain.Issue, comments []domain.IssueComment, labelIds, assigneeIds []string) (bool, error) {
	added := false
	err := p.db.Transaction(func(tx *gorm.DB) error {
		ok, err := number(tx, "issue", issue.RepositoryId, &issue.Number)
		if err != nil || !ok {
			return err
		}
		issue.Id = utils.UUIDv4()
		if err := tx.Omit("Author", "ClosedBy").Create(issue).Error; err != nil {
			return err
		}
		for i := range comments {
			comments[i].Id, comments[i].IssueId = utils.UUIDv4(), issue.Id
			if err := tx.Omit("Author").Create(&comments[i]).Error; err != nil {
				return err
			}
		}
		for _, id := range labelIds {
			if err := tx.Exec("INSERT INTO issue_label (issue_id, label_id) VALUES (?, ?)", issue.Id, id).Error; err != nil {
				return err
			}
		}
		for _, id := range assigneeIds {
			if err := tx.Exec("INSERT INTO issue_assignee (issue_id, user_id) VALUES (?, ?)", issue.Id, id).Error; err != nil {
				return err
			}
		}
		added = true
		return nil
	})
	return added, err
}

func (p *importPers) AddPull(pr *domain.PullRequest, comments []domain.Comment, reviews []domain.ImportedReview, labelIds, assigneeIds []string) (bool, error) {
	added := false
	err := p.db.Transaction(func(tx *gorm.DB) error {
		ok, err := number(tx, "pull_request", pr.RepositoryId, &pr.Number)
		if err != nil || !ok {
			return err
		}
		pr.Id = utils.UUIDv4()
		if err := tx.Omit("Author", "MergedBy").Create(pr).Error; err != nil {
			return err
		}
		for i := range comments {
			comments[i].Id, comments[i].PullRequestId = utils.UUIDv4(), pr.Id
			if err := tx.Omit("Author").Create(&comments[i]).Error; err != nil {
				return err
			}
		}
		for i := range reviews {
			r := &reviews[i].Review
			r.Id, r.PullRequestId = utils.UUIDv4(), pr.Id
			if err := tx.Omit("Author").Create(r).Error; err != nil {
				return err
			}
			for j := range reviews[i].Comments {
				c := &reviews[i].Comments[j]
				c.Id, c.PullRequestId, c.ReviewId = utils.UUIDv4(), pr.Id, &r.Id
				if err := tx.Omit("Author").Create(c).Error; err != nil {
					return err
				}
			}
		}
		for _, id := range labelIds {
			if err := tx.Exec("INSERT INTO pull_request_label (pull_request_id, label_id) VALUES (?, ?)", pr.Id, id).Error; err != nil {
				return err
			}
		}
		for _, id := range assigneeIds {
			if err := tx.Exec("INSERT INTO pull_request_assignee (pull_request_id, user_id) VALUES (?, ?)", pr.Id, id).Error; err != nil {
				return err
			}
		}
		added = true
		return nil
	})
	return added, err
}
