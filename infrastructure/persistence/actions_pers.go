package persistence

import (
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type actionsPers struct{ db *gorm.DB }

func NewActionsPers(db *gorm.DB) *actionsPers { return &actionsPers{db: db} }

func (p *actionsPers) CreateRegistrationToken(t *domain.ActionRegistrationToken) error {
	t.Id = utils.UUIDv4()
	return p.db.Create(t).Error
}

func (p *actionsPers) TakeRegistrationToken(hash string, now time.Time) (*domain.ActionRegistrationToken, error) {
	var t domain.ActionRegistrationToken
	err := p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("token_hash = ? AND expires_at > ?", hash, now).First(&t).Error; err != nil {
			return err
		}
		// Only the request that deletes it may use it.
		res := tx.Where("id = ?", t.Id).Delete(&domain.ActionRegistrationToken{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (p *actionsPers) CreateRunner(r *domain.ActionRunner) error {
	r.Id = utils.UUIDv4()
	return p.db.Create(r).Error
}

func (p *actionsPers) RunnerByTokenHash(hash string) (*domain.ActionRunner, error) {
	var r domain.ActionRunner
	if err := p.db.Where("token_hash = ?", hash).First(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

func (p *actionsPers) Runners(scopeType string, scopeId *string) ([]domain.ActionRunner, error) {
	q := p.db.Where("scope_type = ?", scopeType)
	if scopeId != nil {
		q = q.Where("scope_id = ?", *scopeId)
	}
	var out []domain.ActionRunner
	err := q.Order("created_at").Find(&out).Error
	return out, err
}

func (p *actionsPers) GetRunner(id string) (*domain.ActionRunner, error) {
	var r domain.ActionRunner
	if err := p.db.Where("id = ?", id).First(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

func (p *actionsPers) DeleteRunner(id string) error {
	return p.db.Where("id = ?", id).Delete(&domain.ActionRunner{}).Error
}

func (p *actionsPers) TouchRunner(id, labels, version string, at time.Time) error {
	return p.db.Model(&domain.ActionRunner{}).Where("id = ?", id).
		Updates(map[string]any{"labels": labels, "version": version, "last_seen_at": at}).Error
}

func (p *actionsPers) CreateRun(r *domain.ActionRun) error {
	r.Id = utils.UUIDv4()
	return p.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Exec("UPDATE repository SET last_run_number = last_run_number + 1 WHERE id = ?", r.RepositoryId)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Raw("SELECT last_run_number FROM repository WHERE id = ?", r.RepositoryId).Scan(&r.Number).Error; err != nil {
			return err
		}
		return tx.Create(r).Error
	})
}

func (p *actionsPers) GetRun(repositoryId string, number int) (*domain.ActionRun, error) {
	var r domain.ActionRun
	if err := p.db.Where("repository_id = ? AND number = ?", repositoryId, number).First(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

func (p *actionsPers) GetRunById(id string) (*domain.ActionRun, error) {
	var r domain.ActionRun
	if err := p.db.Where("id = ?", id).First(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

func (p *actionsPers) ListRuns(repositoryId string, offset, limit int) ([]domain.ActionRun, int64, error) {
	q := p.db.Model(&domain.ActionRun{}).Where("repository_id = ?", repositoryId)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []domain.ActionRun
	err := q.Order("number DESC").Offset(offset).Limit(limit).Find(&out).Error
	return out, total, err
}

func (p *actionsPers) QueuedRuns(repositoryIds []string, limit int) ([]domain.ActionRun, error) {
	q := p.db.Where("status = ?", domain.RunQueued)
	if repositoryIds != nil {
		if len(repositoryIds) == 0 {
			return nil, nil
		}
		q = q.Where("repository_id IN ?", repositoryIds)
	}
	var out []domain.ActionRun
	err := q.Order("created_at, number").Limit(limit).Find(&out).Error
	return out, err
}

func (p *actionsPers) ClaimRun(runId, runnerId, tokenHash string, now time.Time) (bool, error) {
	res := p.db.Model(&domain.ActionRun{}).Where("id = ? AND status = ?", runId, domain.RunQueued).
		Updates(map[string]any{"status": domain.RunRunning, "runner_id": runnerId, "token_hash": tokenHash, "started_at": now, "heartbeat_at": now})
	return res.RowsAffected == 1, res.Error
}

func (p *actionsPers) UpdateRun(r *domain.ActionRun, fields ...string) error {
	return p.db.Model(r).Select(fields).Updates(r).Error
}

func (p *actionsPers) RunByTokenHash(hash string) (*domain.ActionRun, error) {
	var r domain.ActionRun
	if err := p.db.Where("token_hash = ? AND status = ?", hash, domain.RunRunning).First(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

func (p *actionsPers) StaleRuns(before time.Time) ([]domain.ActionRun, error) {
	var out []domain.ActionRun
	err := p.db.Where("status = ? AND heartbeat_at < ?", domain.RunRunning, before).Find(&out).Error
	return out, err
}

func (p *actionsPers) UpsertJob(j *domain.ActionJob) error {
	var existing domain.ActionJob
	err := p.db.Where("run_id = ? AND name = ?", j.RunId, j.Name).First(&existing).Error
	if err == gorm.ErrRecordNotFound {
		j.Id = utils.UUIDv4()
		return p.db.Create(j).Error
	} else if err != nil {
		return err
	}
	j.Id = existing.Id
	if j.StartedAt == nil {
		j.StartedAt = existing.StartedAt
	}
	return p.db.Model(j).Select("status", "started_at", "finished_at").Updates(j).Error
}

func (p *actionsPers) Jobs(runId string) ([]domain.ActionJob, error) {
	var out []domain.ActionJob
	err := p.db.Where("run_id = ?", runId).Order("started_at, name").Find(&out).Error
	return out, err
}

func (p *actionsPers) AppendLog(runId string, data string) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		var next int
		if err := tx.Raw("SELECT COALESCE(MAX(seq), 0) + 1 FROM action_log WHERE run_id = ?", runId).Scan(&next).Error; err != nil {
			return err
		}
		return tx.Create(&domain.ActionLog{RunId: runId, Seq: next, Data: data}).Error
	})
}

func (p *actionsPers) Logs(runId string, fromSeq int) ([]domain.ActionLog, error) {
	var out []domain.ActionLog
	err := p.db.Where("run_id = ? AND seq >= ?", runId, fromSeq).Order("seq").Find(&out).Error
	return out, err
}
