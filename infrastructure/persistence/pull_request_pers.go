package persistence

import (
	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"gorm.io/gorm"
)

type pullRequestPers struct {
	db *gorm.DB
}

func NewPullRequestPers(db *gorm.DB) *pullRequestPers {
	return &pullRequestPers{db: db}
}

// Create retries when two pull requests take the same number at once (the
// unique index on repository and number decides).
func (p *pullRequestPers) Create(pr *domain.PullRequest) error {
	var err error
	for range 5 {
		err = p.db.Transaction(func(tx *gorm.DB) error {
			var last int
			if err := tx.Model(&domain.PullRequest{}).Where("repository_id = ?", pr.RepositoryId).
				Select("COALESCE(MAX(number), 0)").Scan(&last).Error; err != nil {
				return err
			}
			pr.Id = utils.UUIDv4()
			pr.Number = last + 1
			return tx.Omit("Author", "MergedBy").Create(pr).Error
		})
		if helperError.Catch(err) != gorm.ErrDuplicatedKey {
			return err
		}
	}
	return err
}

func (p *pullRequestPers) Get(repositoryId string, number int) (*domain.PullRequest, error) {
	var pr domain.PullRequest
	err := p.db.Preload("Author").Preload("MergedBy").Where("repository_id = ? AND number = ?", repositoryId, number).First(&pr).Error
	if err != nil {
		return nil, err
	}
	return &pr, nil
}

func (p *pullRequestPers) List(repositoryId string, state domain.PullRequestState, offset, limit int) ([]domain.PullRequest, map[domain.PullRequestState]int64, error) {
	counts := map[domain.PullRequestState]int64{}
	var rows []struct {
		State domain.PullRequestState
		N     int64
	}
	if err := p.db.Model(&domain.PullRequest{}).Select("state, COUNT(*) AS n").Where("repository_id = ?", repositoryId).
		Group("state").Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		counts[r.State] = r.N
	}
	q := p.db.Preload("Author").Where("repository_id = ?", repositoryId)
	if state != "" {
		q = q.Where("state = ?", state)
	}
	var prs []domain.PullRequest
	err := q.Order("number DESC").Offset(offset).Limit(limit).Find(&prs).Error
	return prs, counts, err
}

func (p *pullRequestPers) OpenFor(repositoryId, source, target string) (*domain.PullRequest, error) {
	var pr domain.PullRequest
	err := p.db.Where("repository_id = ? AND source_branch = ? AND target_branch = ? AND state = ?",
		repositoryId, source, target, domain.PullRequestOpen).First(&pr).Error
	if err != nil {
		return nil, err
	}
	return &pr, nil
}

func (p *pullRequestPers) Update(id string, from domain.PullRequestState, fields map[string]any) (bool, error) {
	res := p.db.Model(&domain.PullRequest{}).Where("id = ? AND state = ?", id, from).Updates(fields)
	return res.RowsAffected == 1, res.Error
}
