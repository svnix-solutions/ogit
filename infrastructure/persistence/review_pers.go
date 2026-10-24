package persistence

import (
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type reviewPers struct{ db *gorm.DB }

func NewReviewPers(db *gorm.DB) *reviewPers { return &reviewPers{db: db} }

func (p *reviewPers) CreateReview(review *domain.Review, comments []domain.Comment) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		review.Id = utils.UUIDv4()
		if err := tx.Omit("Author").Create(review).Error; err != nil {
			return err
		}
		for i := range comments {
			comments[i].Id = utils.UUIDv4()
			comments[i].ReviewId = &review.Id
			comments[i].PullRequestId = review.PullRequestId
			if err := tx.Omit("Author").Create(&comments[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (p *reviewPers) Reviews(pullRequestId string) ([]domain.Review, error) {
	var reviews []domain.Review
	err := p.db.Preload("Author").Where("pull_request_id = ?", pullRequestId).Order("created_at, id").Find(&reviews).Error
	return reviews, err
}

func (p *reviewPers) CreateComment(c *domain.Comment) error {
	c.Id = utils.UUIDv4()
	return p.db.Omit("Author").Create(c).Error
}

func (p *reviewPers) Comment(pullRequestId, id string) (*domain.Comment, error) {
	var c domain.Comment
	if err := p.db.Where("pull_request_id = ? AND id = ?", pullRequestId, id).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (p *reviewPers) UpdateComment(id, body string) error {
	return p.db.Model(&domain.Comment{}).Where("id = ?", id).Updates(map[string]any{"body": body, "updated_at": time.Now()}).Error
}

func (p *reviewPers) DeleteComment(id string) error {
	return p.db.Where("id = ?", id).Delete(&domain.Comment{}).Error
}

func (p *reviewPers) Comments(pullRequestId string) ([]domain.Comment, error) {
	var comments []domain.Comment
	err := p.db.Preload("Author").Where("pull_request_id = ?", pullRequestId).Order("created_at, id").Find(&comments).Error
	return comments, err
}

type labelPers struct{ db *gorm.DB }

func NewLabelPers(db *gorm.DB) *labelPers { return &labelPers{db: db} }

func (p *labelPers) List(repositoryId string) ([]domain.Label, error) {
	var labels []domain.Label
	err := p.db.Where("repository_id = ?", repositoryId).Order("name").Find(&labels).Error
	return labels, err
}

func (p *labelPers) Create(l *domain.Label) error {
	l.Id = utils.UUIDv4()
	return p.db.Create(l).Error
}

func (p *labelPers) Get(repositoryId, id string) (*domain.Label, error) {
	var l domain.Label
	if err := p.db.Where("repository_id = ? AND id = ?", repositoryId, id).First(&l).Error; err != nil {
		return nil, err
	}
	return &l, nil
}

func (p *labelPers) Update(id string, fields map[string]any) error {
	return p.db.Model(&domain.Label{}).Where("id = ?", id).Updates(fields).Error
}

// Delete removes the label from pull requests too (SQLite does not enforce
// the cascade here).
func (p *labelPers) Delete(id string) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM pull_request_label WHERE label_id = ?", id).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&domain.Label{}).Error
	})
}

func (p *labelPers) Set(pullRequestId string, labelIds []string) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM pull_request_label WHERE pull_request_id = ?", pullRequestId).Error; err != nil {
			return err
		}
		for _, id := range labelIds {
			if err := tx.Exec("INSERT INTO pull_request_label (pull_request_id, label_id) VALUES (?, ?)", pullRequestId, id).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (p *labelPers) For(pullRequestIds []string) (map[string][]domain.Label, error) {
	out := map[string][]domain.Label{}
	if len(pullRequestIds) == 0 {
		return out, nil
	}
	var rows []struct {
		PullRequestId string
		domain.Label
	}
	err := p.db.Table("pull_request_label").
		Select("pull_request_label.pull_request_id, label.*").
		Joins("JOIN label ON label.id = pull_request_label.label_id").
		Where("pull_request_label.pull_request_id IN ?", pullRequestIds).Order("label.name").Scan(&rows).Error
	for _, r := range rows {
		out[r.PullRequestId] = append(out[r.PullRequestId], r.Label)
	}
	return out, err
}
