package persistence

import (
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type requestedReviewerPers struct{ db *gorm.DB }

func NewRequestedReviewerPers(db *gorm.DB) *requestedReviewerPers {
	return &requestedReviewerPers{db: db}
}

func (p *requestedReviewerPers) For(pullRequestIds []string) (map[string][]domain.User, error) {
	out := map[string][]domain.User{}
	if len(pullRequestIds) == 0 {
		return out, nil
	}
	var rows []domain.RequestedReviewer
	if err := p.db.Preload("User").Where("pull_request_id IN ?", pullRequestIds).Order("created_at").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.PullRequestId] = append(out[r.PullRequestId], r.User)
	}
	return out, nil
}

// Set keeps existing requests (and their date) and adds or removes the
// difference.
func (p *requestedReviewerPers) Set(pullRequestId string, userIds []string, requestedBy string) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		q := tx.Where("pull_request_id = ?", pullRequestId)
		if len(userIds) > 0 {
			q = q.Where("user_id NOT IN ?", userIds)
		}
		if err := q.Delete(&domain.RequestedReviewer{}).Error; err != nil {
			return err
		}
		var existing []string
		if err := tx.Model(&domain.RequestedReviewer{}).Where("pull_request_id = ?", pullRequestId).Pluck("user_id", &existing).Error; err != nil {
			return err
		}
		have := map[string]bool{}
		for _, id := range existing {
			have[id] = true
		}
		by := &requestedBy
		for _, id := range userIds {
			if !have[id] {
				if err := tx.Omit("User").Create(&domain.RequestedReviewer{PullRequestId: pullRequestId, UserId: id, RequestedById: by}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (p *requestedReviewerPers) Remove(pullRequestId, userId string) (bool, error) {
	res := p.db.Where("pull_request_id = ? AND user_id = ?", pullRequestId, userId).Delete(&domain.RequestedReviewer{})
	return res.RowsAffected == 1, res.Error
}
