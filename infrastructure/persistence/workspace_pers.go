package persistence

import (
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type workspacePers struct{ db *gorm.DB }

func NewWorkspacePers(db *gorm.DB) *workspacePers { return &workspacePers{db: db} }

func (p *workspacePers) Create(w *domain.Workspace) error {
	w.Id = utils.UUIDv4()
	err := p.db.Create(w).Error
	if helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return domain.ErrWorkspaceExists
	}
	return err
}

func (p *workspacePers) Get(userId, id string) (*domain.Workspace, error) {
	var w domain.Workspace
	if err := p.db.Where("user_id = ? AND id = ?", userId, id).First(&w).Error; err != nil {
		return nil, err
	}
	return &w, nil
}

func (p *workspacePers) List(userId, repositoryId string) ([]domain.Workspace, error) {
	q := p.db.Where("user_id = ?", userId)
	if repositoryId != "" {
		q = q.Where("repository_id = ?", repositoryId)
	}
	var out []domain.Workspace
	err := q.Order("last_used_at DESC, id").Find(&out).Error
	return out, err
}

// Start counts and moves in one UPDATE, which is enough on SQLite (writes
// are serialized). On Postgres two UPDATEs of different rows wouldn't see
// each other, so the user's row is locked first.
func (p *workspacePers) Start(userId, id string, max int, now time.Time) (bool, error) {
	started := false
	err := p.db.Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			var locked string
			if err := tx.Model(&domain.User{}).Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ?", userId).Select("id").Scan(&locked).Error; err != nil {
				return err
			}
		}
		res := tx.Exec(`UPDATE workspace SET state = ?, last_used_at = ?, updated_at = ?
			WHERE id = ? AND user_id = ? AND state = ?
			AND (SELECT COUNT(*) FROM workspace active WHERE active.user_id = ? AND active.state <> ?) < ?`,
			domain.WorkspaceStarting, now, now, id, userId, domain.WorkspaceStopped, userId, domain.WorkspaceStopped, max)
		started = res.RowsAffected == 1
		return res.Error
	})
	return started, err
}

func (p *workspacePers) Stop(userId, id string, now time.Time) error {
	return p.db.Model(&domain.Workspace{}).
		Where("id = ? AND user_id = ? AND state IN ?", id, userId, []domain.WorkspaceState{domain.WorkspaceStarting, domain.WorkspaceRunning}).
		Updates(map[string]any{"state": domain.WorkspaceStopped, "last_used_at": now}).Error
}

func (p *workspacePers) Delete(userId, id string) (bool, error) {
	res := p.db.Where("id = ? AND user_id = ? AND state = ?", id, userId, domain.WorkspaceStopped).Delete(&domain.Workspace{})
	return res.RowsAffected == 1, res.Error
}
