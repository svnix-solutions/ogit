package persistence

import (
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	r.Id, r.ApiId = utils.UUIDv4(), newApiId()
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

func (p *actionsPers) ListRuns(repositoryId string, f domain.RunFilter, offset, limit int) ([]domain.ActionRun, int64, error) {
	q := p.db.Model(&domain.ActionRun{}).Where("repository_id = ?", repositoryId)
	if f.WorkflowPath != "" {
		q = q.Where("workflow_path = ?", f.WorkflowPath)
	}
	if f.Ref != "" {
		q = q.Where("ref = ?", f.Ref)
	}
	if f.Event != "" {
		q = q.Where("event = ?", f.Event)
	}
	switch f.Status {
	case "":
	case "waiting":
		q = q.Where("status = ? AND needs_approval = ?", domain.RunQueued, true)
	case domain.RunQueued:
		q = q.Where("status = ? AND needs_approval = ?", domain.RunQueued, false)
	default:
		q = q.Where("status = ?", f.Status)
	}
	if f.PullNumber > 0 {
		q = q.Where("pull_number = ?", f.PullNumber)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []domain.ActionRun
	err := q.Order("number DESC").Offset(offset).Limit(limit).Find(&out).Error
	return out, total, err
}

func (p *actionsPers) LatestRun(repositoryId, workflowPath, ref string) (*domain.ActionRun, error) {
	var r domain.ActionRun
	err := p.db.Where("repository_id = ? AND workflow_path = ? AND ref = ? AND status IN ?", repositoryId, workflowPath, ref,
		[]string{domain.RunSuccess, domain.RunFailure, domain.RunCancelled}).Order("number DESC").First(&r).Error
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (p *actionsPers) QueuedRuns(repositoryIds []string, limit int) ([]domain.ActionRun, error) {
	q := p.db.Where("status = ? AND needs_approval = ?", domain.RunQueued, false)
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
	res := p.db.Model(&domain.ActionRun{}).Where("id = ? AND status = ? AND needs_approval = ?", runId, domain.RunQueued, false).
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

func (p *actionsPers) Secrets(scopeType, scopeId string) ([]domain.ActionSecret, error) {
	var out []domain.ActionSecret
	err := p.db.Where("scope_type = ? AND scope_id = ?", scopeType, scopeId).Order("name").Find(&out).Error
	return out, err
}

func (p *actionsPers) PutSecret(s *domain.ActionSecret) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		var existing domain.ActionSecret
		err := tx.Where("scope_type = ? AND scope_id = ? AND name = ?", s.ScopeType, s.ScopeId, s.Name).First(&existing).Error
		if err == gorm.ErrRecordNotFound {
			s.Id = utils.UUIDv4()
			return tx.Create(s).Error
		} else if err != nil {
			return err
		}
		s.Id, s.CreatedAt = existing.Id, existing.CreatedAt
		return tx.Model(s).Select("value", "updated_at").Updates(s).Error
	})
}

func (p *actionsPers) DeleteSecret(scopeType, scopeId, name string) (bool, error) {
	res := p.db.Where("scope_type = ? AND scope_id = ? AND name = ?", scopeType, scopeId, name).Delete(&domain.ActionSecret{})
	return res.RowsAffected > 0, res.Error
}

func (p *actionsPers) Variables(scopeType, scopeId string) ([]domain.ActionVariable, error) {
	var out []domain.ActionVariable
	err := p.db.Where("scope_type = ? AND scope_id = ?", scopeType, scopeId).Order("name").Find(&out).Error
	return out, err
}

func (p *actionsPers) PutVariable(v *domain.ActionVariable) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		var existing domain.ActionVariable
		err := tx.Where("scope_type = ? AND scope_id = ? AND name = ?", v.ScopeType, v.ScopeId, v.Name).First(&existing).Error
		if err == gorm.ErrRecordNotFound {
			v.Id = utils.UUIDv4()
			return tx.Create(v).Error
		} else if err != nil {
			return err
		}
		v.Id, v.CreatedAt = existing.Id, existing.CreatedAt
		return tx.Model(v).Select("value", "updated_at").Updates(v).Error
	})
}

func (p *actionsPers) DeleteVariable(scopeType, scopeId, name string) (bool, error) {
	res := p.db.Where("scope_type = ? AND scope_id = ? AND name = ?", scopeType, scopeId, name).Delete(&domain.ActionVariable{})
	return res.RowsAffected > 0, res.Error
}

func (p *actionsPers) ReplaceSchedules(repositoryId string, schedules []domain.ActionSchedule) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("repository_id = ?", repositoryId).Delete(&domain.ActionSchedule{}).Error; err != nil {
			return err
		}
		for i := range schedules {
			schedules[i].Id = utils.UUIDv4()
			schedules[i].RepositoryId = repositoryId
			if err := tx.Create(&schedules[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (p *actionsPers) DueSchedules(now time.Time) ([]domain.ActionSchedule, error) {
	var out []domain.ActionSchedule
	err := p.db.Where("next_run_at <= ?", now).Order("next_run_at").Limit(100).Find(&out).Error
	return out, err
}

func (p *actionsPers) ClaimSchedule(id string, prev, next time.Time) (bool, error) {
	res := p.db.Model(&domain.ActionSchedule{}).Where("id = ? AND next_run_at = ?", id, prev).Update("next_run_at", next)
	return res.RowsAffected == 1, res.Error
}

func (p *actionsPers) HasMergedPull(repositoryId, userId string) (bool, error) {
	var n int64
	err := p.db.Model(&domain.PullRequest{}).Where("repository_id = ? AND author_id = ? AND state = ?", repositoryId, userId, domain.PullRequestMerged).Count(&n).Error
	return n > 0, err
}

func (p *actionsPers) CreateArtifact(a *domain.ActionArtifact) error {
	a.Id, a.ApiId = utils.UUIDv4(), newApiId()
	return p.db.Create(a).Error
}

func (p *actionsPers) Artifact(runId, name string) (*domain.ActionArtifact, error) {
	var a domain.ActionArtifact
	if err := p.db.Where("run_id = ? AND name = ?", runId, name).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (p *actionsPers) ArtifactByApiId(repositoryId string, apiId int64) (*domain.ActionArtifact, error) {
	var a domain.ActionArtifact
	if err := p.db.Where("repository_id = ? AND api_id = ?", repositoryId, apiId).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (p *actionsPers) Artifacts(runId string) ([]domain.ActionArtifact, error) {
	var out []domain.ActionArtifact
	err := p.db.Where("run_id = ? AND ready = ?", runId, true).Order("name").Find(&out).Error
	return out, err
}

func (p *actionsPers) ArtifactById(id string) (*domain.ActionArtifact, error) {
	var a domain.ActionArtifact
	if err := p.db.Where("id = ?", id).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (p *actionsPers) SetArtifactSize(id string, size int64) error {
	return p.db.Model(&domain.ActionArtifact{}).Where("id = ?", id).Update("size", size).Error
}

func (p *actionsPers) AddArtifactBlock(artifactId, blockId string) error {
	return p.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&domain.ActionArtifactBlock{ArtifactId: artifactId, BlockId: blockId}).Error
}

func (p *actionsPers) ArtifactBlocks(artifactId string) ([]string, error) {
	var out []string
	err := p.db.Model(&domain.ActionArtifactBlock{}).Where("artifact_id = ?", artifactId).Pluck("block_id", &out).Error
	return out, err
}

func (p *actionsPers) ForgetArtifactBlocks(artifactId string) error {
	return p.db.Where("artifact_id = ?", artifactId).Delete(&domain.ActionArtifactBlock{}).Error
}

func (p *actionsPers) MarkArtifactReady(id string, size int64) error {
	return p.db.Model(&domain.ActionArtifact{}).Where("id = ?", id).Updates(map[string]any{"ready": true, "size": size}).Error
}

func (p *actionsPers) DeleteArtifact(id string) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("artifact_id = ?", id).Delete(&domain.ActionArtifactBlock{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&domain.ActionArtifact{}).Error
	})
}

func (p *actionsPers) ExpiredArtifacts(now time.Time, limit int) ([]domain.ActionArtifact, error) {
	var out []domain.ActionArtifact
	// Uploads never finalized go after a day.
	err := p.db.Where("expires_at < ? OR (ready = ? AND created_at < ?)", now, false, now.Add(-24*time.Hour)).Order("expires_at").Limit(limit).Find(&out).Error
	return out, err
}

func (p *actionsPers) RunByApiId(repositoryId string, apiId int64) (*domain.ActionRun, error) {
	var r domain.ActionRun
	if err := p.db.Where("repository_id = ? AND api_id = ?", repositoryId, apiId).First(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}
