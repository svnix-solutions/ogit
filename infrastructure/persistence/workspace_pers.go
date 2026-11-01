package persistence

import (
	"errors"
	"math"
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type workspacePers struct{ db *gorm.DB }

func NewWorkspacePers(db *gorm.DB) *workspacePers { return &workspacePers{db: db} }

// lockUser serializes one user's counted updates on Postgres, where two
// statements on different rows don't see each other. SQLite serializes writes.
func lockUser(tx *gorm.DB, userId string) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	var locked string
	return tx.Model(&domain.User{}).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", userId).Select("id").Scan(&locked).Error
}

func (p *workspacePers) Create(w *domain.Workspace, maxTotal int) (bool, error) {
	w.Id = utils.UUIDv4()
	w.CreatedAt, w.UpdatedAt = w.LastUsedAt, w.LastUsedAt
	if maxTotal <= 0 {
		maxTotal = math.MaxInt32
	}
	created := false
	err := p.db.Transaction(func(tx *gorm.DB) error {
		if err := lockUser(tx, w.UserId); err != nil {
			return err
		}
		res := tx.Exec(`INSERT INTO workspace (id, repository_id, user_id, branch, state, last_used_at, created_at, updated_at)
			SELECT ?, ?, ?, ?, ?, ?, ?, ?
			WHERE (SELECT COUNT(*) FROM workspace mine WHERE mine.user_id = ?) < ?`,
			w.Id, w.RepositoryId, w.UserId, w.Branch, w.State, w.LastUsedAt, w.CreatedAt, w.UpdatedAt, w.UserId, maxTotal)
		created = res.RowsAffected == 1
		return res.Error
	})
	if helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return false, domain.ErrWorkspaceExists
	}
	return created, err
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

func (p *workspacePers) GetAny(id string) (*domain.Workspace, error) {
	var w domain.Workspace
	if err := p.db.Where("id = ?", id).First(&w).Error; err != nil {
		return nil, err
	}
	return &w, nil
}

func (p *workspacePers) All() ([]domain.Workspace, error) {
	var out []domain.Workspace
	err := p.db.Order("last_used_at DESC, id").Find(&out).Error
	return out, err
}

// Start counts and moves in one UPDATE.
func (p *workspacePers) Start(userId, id string, max int, now time.Time) (bool, error) {
	started := false
	err := p.db.Transaction(func(tx *gorm.DB) error {
		if err := lockUser(tx, userId); err != nil {
			return err
		}
		res := tx.Exec(`UPDATE workspace SET state = ?, failure = '', last_used_at = ?, updated_at = ?
			WHERE id = ? AND user_id = ? AND state = ?
			AND (SELECT COUNT(*) FROM workspace active WHERE active.user_id = ? AND active.state <> ?) < ?`,
			domain.WorkspaceStarting, now, now, id, userId, domain.WorkspaceStopped, userId, domain.WorkspaceStopped, max)
		started = res.RowsAffected == 1
		return res.Error
	})
	return started, err
}

var workspaceAlive = []domain.WorkspaceState{domain.WorkspaceStarting, domain.WorkspaceRunning}
var workspaceActive = []domain.WorkspaceState{domain.WorkspaceStarting, domain.WorkspaceRunning, domain.WorkspaceStopping}

func (p *workspacePers) Stop(id string, now time.Time) error {
	return stopWorkspace(p.db, id, now)
}

func stopWorkspace(db *gorm.DB, id string, now time.Time) error {
	return db.Exec(`UPDATE workspace SET state = CASE WHEN agent_id IS NULL THEN ? ELSE ? END,
		token_hash = NULL, token_expires_at = NULL, last_used_at = ?, updated_at = ?
		WHERE id = ? AND state IN ?`,
		domain.WorkspaceStopped, domain.WorkspaceStopping, now, now, id, workspaceAlive).Error
}

func (p *workspacePers) Delete(id string, force bool) (bool, error) {
	deleted := false
	err := p.db.Transaction(func(tx *gorm.DB) error {
		q := tx.Where("id = ?", id)
		if !force {
			q = q.Where("state = ?", domain.WorkspaceStopped)
		}
		var w domain.Workspace
		if err := q.First(&w).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if err := queueCleanups(tx, "id = ?", id); err != nil {
			return err
		}
		deleted = true
		return tx.Where("id = ?", id).Delete(&domain.Workspace{}).Error
	})
	return deleted, err
}

// queueCleanups leaves the agents of the workspaces matching where a cleanup,
// before those rows are deleted.
func queueCleanups(tx *gorm.DB, where string, arg string) error {
	return tx.Exec(`INSERT INTO workspace_cleanup (workspace_id, agent_id, created_at)
		SELECT id, agent_id, ? FROM workspace WHERE agent_id IS NOT NULL AND `+where, time.Now().UTC(), arg).Error
}

func (p *workspacePers) ByTokenHash(hash string, now time.Time) (*domain.Workspace, error) {
	var w domain.Workspace
	err := p.db.Where("token_hash = ? AND token_expires_at > ? AND state IN ?", hash, now, workspaceAlive).First(&w).Error
	if err != nil {
		return nil, err
	}
	return &w, nil
}

func (p *workspacePers) CreateAgentToken(t *domain.WorkspaceAgentToken) error {
	t.Id = utils.UUIDv4()
	return p.db.Create(t).Error
}

func (p *workspacePers) AgentTokens(now time.Time) ([]domain.WorkspaceAgentToken, map[string]int64, error) {
	var out []domain.WorkspaceAgentToken
	if err := p.db.Where("reusable AND (expires_at IS NULL OR expires_at > ?)", now).Order("created_at").Find(&out).Error; err != nil {
		return nil, nil, err
	}
	var rows []struct {
		RegistrationTokenId string
		N                   int64
	}
	err := p.db.Model(&domain.WorkspaceAgent{}).Select("registration_token_id, COUNT(*) AS n").
		Where("registration_token_id IS NOT NULL").Group("registration_token_id").Scan(&rows).Error
	if err != nil {
		return nil, nil, err
	}
	counts := map[string]int64{}
	for _, r := range rows {
		counts[r.RegistrationTokenId] = r.N
	}
	return out, counts, nil
}

func (p *workspacePers) RevokeAgentToken(id string) (bool, error) {
	res := p.db.Where("id = ?", id).Delete(&domain.WorkspaceAgentToken{})
	return res.RowsAffected == 1, res.Error
}

func (p *workspacePers) TakeAgentToken(hash string, now time.Time) (*domain.WorkspaceAgentToken, error) {
	var t domain.WorkspaceAgentToken
	err := p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("token_hash = ? AND (expires_at IS NULL OR expires_at > ?)", hash, now).First(&t).Error; err != nil {
			return err
		}
		if t.Reusable {
			t.LastUsedAt = &now
			return tx.Model(&domain.WorkspaceAgentToken{}).Where("id = ?", t.Id).Update("last_used_at", now).Error
		}
		// Only the request that deletes it may use it.
		res := tx.Where("id = ?", t.Id).Delete(&domain.WorkspaceAgentToken{})
		if res.Error == nil && res.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return res.Error
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (p *workspacePers) CreateAgent(a *domain.WorkspaceAgent) error {
	a.Id = utils.UUIDv4()
	return p.db.Create(a).Error
}

func (p *workspacePers) AgentByTokenHash(hash string) (*domain.WorkspaceAgent, error) {
	var a domain.WorkspaceAgent
	if err := p.db.Where("token_hash = ?", hash).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (p *workspacePers) GetAgent(id string) (*domain.WorkspaceAgent, error) {
	var a domain.WorkspaceAgent
	if err := p.db.Where("id = ?", id).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (p *workspacePers) Agents() ([]domain.WorkspaceAgent, error) {
	var out []domain.WorkspaceAgent
	err := p.db.Order("name, id").Find(&out).Error
	return out, err
}

func (p *workspacePers) DeleteAgent(id, failure string, now time.Time) (bool, error) {
	deleted := false
	err := p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE workspace SET failure = CASE WHEN state = ? THEN failure ELSE ? END, state = ?, agent_id = NULL,
			token_hash = NULL, token_expires_at = NULL, updated_at = ? WHERE agent_id = ?`,
			domain.WorkspaceStopped, failure, domain.WorkspaceStopped, now, id).Error; err != nil {
			return err
		}
		if err := tx.Where("agent_id = ?", id).Delete(&domain.WorkspaceCleanup{}).Error; err != nil {
			return err
		}
		res := tx.Where("id = ?", id).Delete(&domain.WorkspaceAgent{})
		deleted = res.RowsAffected == 1
		return res.Error
	})
	return deleted, err
}

func (p *workspacePers) TouchAgent(id, version string, now time.Time) error {
	return p.db.Model(&domain.WorkspaceAgent{}).Where("id = ?", id).
		Updates(map[string]any{"version": version, "last_seen_at": now}).Error
}

func (p *workspacePers) ForAgent(agentId string) ([]domain.Workspace, error) {
	var out []domain.Workspace
	err := p.db.Where("agent_id = ? AND state IN ?", agentId, workspaceActive).Order("updated_at, id").Find(&out).Error
	return out, err
}

func (p *workspacePers) MarkRunning(agentId, id string, now time.Time) error {
	return p.db.Model(&domain.Workspace{}).Where("id = ? AND agent_id = ? AND state = ?", id, agentId, domain.WorkspaceStarting).
		Updates(map[string]any{"state": domain.WorkspaceRunning, "last_used_at": now}).Error
}

func (p *workspacePers) ExtendTokens(agentId string, ids []string, expires time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	return p.db.Model(&domain.Workspace{}).
		Where("agent_id = ? AND id IN ? AND state IN ? AND token_hash IS NOT NULL", agentId, ids, workspaceAlive).
		Update("token_expires_at", expires).Error
}

func (p *workspacePers) MarkStopped(agentId string, keep []string, now time.Time) error {
	q := p.db.Model(&domain.Workspace{}).Where("agent_id = ? AND state = ?", agentId, domain.WorkspaceStopping)
	if len(keep) > 0 {
		q = q.Where("id NOT IN ?", keep)
	}
	return q.Updates(map[string]any{"state": domain.WorkspaceStopped, "last_used_at": now}).Error
}

func (p *workspacePers) Fail(agentId, id, failure string, now time.Time) error {
	return p.db.Model(&domain.Workspace{}).Where("id = ? AND agent_id = ? AND state IN ?", id, agentId, workspaceActive).
		Updates(map[string]any{"state": domain.WorkspaceStopped, "failure": failure, "token_hash": nil, "token_expires_at": nil, "last_used_at": now}).Error
}

func (p *workspacePers) Claim(agentId string, n int, now time.Time) ([]domain.Workspace, error) {
	if n <= 0 {
		return nil, nil
	}
	var free []domain.Workspace
	err := p.db.Where("state = ? AND agent_id IS NULL", domain.WorkspaceStarting).Order("updated_at, id").Limit(n).Find(&free).Error
	if err != nil {
		return nil, err
	}
	var claimed []domain.Workspace
	for _, w := range free {
		res := p.db.Model(&domain.Workspace{}).Where("id = ? AND state = ? AND agent_id IS NULL", w.Id, domain.WorkspaceStarting).
			Updates(map[string]any{"agent_id": agentId, "updated_at": now})
		if res.Error != nil {
			return claimed, res.Error
		}
		if res.RowsAffected == 1 {
			w.AgentId = &agentId
			claimed = append(claimed, w)
		}
	}
	return claimed, nil
}

func (p *workspacePers) SetToken(agentId, id, hash string, expires, now time.Time) (bool, error) {
	res := p.db.Model(&domain.Workspace{}).Where("id = ? AND agent_id = ? AND state = ?", id, agentId, domain.WorkspaceStarting).
		Updates(map[string]any{"token_hash": hash, "token_expires_at": expires})
	return res.RowsAffected == 1, res.Error
}

func (p *workspacePers) Cleanups(agentId string) ([]string, error) {
	var ids []string
	err := p.db.Model(&domain.WorkspaceCleanup{}).Where("agent_id = ?", agentId).Order("created_at, workspace_id").Pluck("workspace_id", &ids).Error
	return ids, err
}

func (p *workspacePers) AckCleanups(agentId string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return p.db.Where("agent_id = ? AND workspace_id IN ?", agentId, ids).Delete(&domain.WorkspaceCleanup{}).Error
}

func (p *workspacePers) FailLost(before time.Time, failure string, now time.Time) (int64, error) {
	res := p.db.Model(&domain.Workspace{}).
		Where("state IN ? AND agent_id IN (?)", workspaceActive, p.db.Model(&domain.WorkspaceAgent{}).Select("id").Where("last_seen_at < ?", before)).
		Updates(map[string]any{"state": domain.WorkspaceStopped, "failure": failure, "token_hash": nil, "token_expires_at": nil})
	return res.RowsAffected, res.Error
}

func (p *workspacePers) FailStuck(before time.Time, failure string, now time.Time) (int64, error) {
	res := p.db.Model(&domain.Workspace{}).Where("state = ? AND last_used_at < ?", domain.WorkspaceStarting, before).
		Updates(map[string]any{"state": domain.WorkspaceStopped, "failure": failure, "token_hash": nil, "token_expires_at": nil})
	return res.RowsAffected, res.Error
}

func (p *workspacePers) MarkAccessLost(id, failure string, now time.Time) (bool, error) {
	marked := false
	err := p.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&domain.Workspace{}).Where("id = ? AND access_lost_at IS NULL", id).
			Updates(map[string]any{"access_lost_at": now, "failure": failure})
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		marked = true
		return stopWorkspace(tx, id, now)
	})
	return marked, err
}

func (p *workspacePers) ClearAccessLost(id, failure string, now time.Time) error {
	return p.db.Exec(`UPDATE workspace SET access_lost_at = NULL, failure = CASE WHEN failure = ? THEN '' ELSE failure END, updated_at = ?
		WHERE id = ? AND access_lost_at IS NOT NULL`, failure, now, id).Error
}

func (p *workspacePers) DeleteExpiredAgentTokens(now time.Time) error {
	return p.db.Where("expires_at IS NOT NULL AND expires_at < ?", now).Delete(&domain.WorkspaceAgentToken{}).Error
}
