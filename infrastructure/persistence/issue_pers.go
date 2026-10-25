package persistence

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"gorm.io/gorm"
)

type issuePers struct{ db *gorm.DB }

func NewIssuePers(db *gorm.DB) *issuePers { return &issuePers{db: db} }

func (p *issuePers) Create(issue *domain.Issue) error {
	var err error
	for range 5 {
		err = p.db.Transaction(func(tx *gorm.DB) error {
			n, err := nextNumber(tx, issue.RepositoryId)
			if err != nil {
				return err
			}
			issue.Id, issue.Number = utils.UUIDv4(), n
			return tx.Omit("Author", "ClosedBy").Create(issue).Error
		})
		if helperError.Catch(err) != gorm.ErrDuplicatedKey {
			return err
		}
	}
	return err
}

func (p *issuePers) Get(repositoryId string, number int) (*domain.Issue, error) {
	var issue domain.Issue
	err := p.db.Preload("Author").Preload("ClosedBy").Where("repository_id = ? AND number = ?", repositoryId, number).First(&issue).Error
	if err != nil {
		return nil, err
	}
	return &issue, nil
}

// escapeLike escapes LIKE wildcards (with \ as the escape character).
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (p *issuePers) List(repositoryId string, f domain.IssueFilter, offset, limit int) ([]domain.Issue, map[domain.IssueState]int64, error) {
	filtered := func() *gorm.DB {
		q := p.db.Model(&domain.Issue{}).Where("repository_id = ?", repositoryId)
		if f.LabelId != "" {
			q = q.Where("id IN (SELECT issue_id FROM issue_label WHERE label_id = ?)", f.LabelId)
		}
		if f.AuthorId != "" {
			q = q.Where("author_id = ?", f.AuthorId)
		}
		if f.Query != "" {
			like := "%" + escapeLike(strings.ToLower(f.Query)) + "%"
			q = q.Where(`(LOWER(title) LIKE ? ESCAPE '\' OR LOWER(body) LIKE ? ESCAPE '\')`, like, like)
		}
		return q
	}
	counts := map[domain.IssueState]int64{}
	var rows []struct {
		State domain.IssueState
		N     int64
	}
	if err := filtered().Select("state, COUNT(*) AS n").Group("state").Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		counts[r.State] = r.N
	}
	q := filtered().Preload("Author")
	if f.State != "" {
		q = q.Where("state = ?", f.State)
	}
	var issues []domain.Issue
	err := q.Order("number DESC").Offset(offset).Limit(limit).Find(&issues).Error
	return issues, counts, err
}

func (p *issuePers) Update(id string, fields map[string]any) error {
	return p.db.Model(&domain.Issue{}).Where("id = ?", id).Updates(fields).Error
}

// Delete removes the issue with its comments and labels (SQLite does not
// enforce the cascades here).
func (p *issuePers) Delete(id string) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		for _, q := range []string{"DELETE FROM issue_label WHERE issue_id = ?", "DELETE FROM issue_comment WHERE issue_id = ?", "DELETE FROM issue WHERE id = ?"} {
			if err := tx.Exec(q, id).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (p *issuePers) CreateComment(c *domain.IssueComment) error {
	c.Id = utils.UUIDv4()
	return p.db.Omit("Author").Create(c).Error
}

func (p *issuePers) Comment(issueId, id string) (*domain.IssueComment, error) {
	var c domain.IssueComment
	if err := p.db.Preload("Author").Where("issue_id = ? AND id = ?", issueId, id).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (p *issuePers) Comments(issueId string) ([]domain.IssueComment, error) {
	var out []domain.IssueComment
	err := p.db.Preload("Author").Where("issue_id = ?", issueId).Order("created_at, id").Find(&out).Error
	return out, err
}

func (p *issuePers) UpdateComment(id, body string) error {
	return p.db.Model(&domain.IssueComment{}).Where("id = ?", id).Updates(map[string]any{"body": body, "updated_at": time.Now()}).Error
}

func (p *issuePers) DeleteComment(id string) error {
	return p.db.Where("id = ?", id).Delete(&domain.IssueComment{}).Error
}

func (p *issuePers) SetLabels(issueId string, labelIds []string) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM issue_label WHERE issue_id = ?", issueId).Error; err != nil {
			return err
		}
		for _, id := range labelIds {
			if err := tx.Exec("INSERT INTO issue_label (issue_id, label_id) VALUES (?, ?)", issueId, id).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (p *issuePers) Labels(issueIds []string) (map[string][]domain.Label, error) {
	out := map[string][]domain.Label{}
	if len(issueIds) == 0 {
		return out, nil
	}
	var rows []struct {
		IssueId string
		domain.Label
	}
	err := p.db.Table("issue_label").Select("issue_label.issue_id, label.*").
		Joins("JOIN label ON label.id = issue_label.label_id").
		Where("issue_label.issue_id IN ?", issueIds).Order("label.name").Scan(&rows).Error
	for _, r := range rows {
		out[r.IssueId] = append(out[r.IssueId], r.Label)
	}
	return out, err
}
