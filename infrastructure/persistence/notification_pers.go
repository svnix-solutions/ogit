package persistence

import (
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type notificationPers struct{ db *gorm.DB }

func NewNotificationPers(db *gorm.DB) *notificationPers { return &notificationPers{db: db} }

// Upsert inserts or, on the (user, thread) unique key, updates in one
// statement, so concurrent events on a thread never make two rows.
func (p *notificationPers) Upsert(n *domain.Notification) error {
	now := time.Now()
	n.Id, n.Unread, n.UpdatedAt, n.CreatedAt = utils.UUIDv4(), true, now, now
	return p.db.Omit("Repository", "Actor").Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "thread_type"}, {Name: "thread_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"title", "url", "thread_number", "reason", "participating", "summary", "actor_id", "unread", "updated_at"}),
	}).Create(n).Error
}

func (p *notificationPers) filtered(userId string, f domain.NotificationFilter) *gorm.DB {
	q := p.db.Model(&domain.Notification{}).Where("user_id = ?", userId)
	if !f.All {
		q = q.Where("unread = ?", true)
	}
	if f.Participating {
		q = q.Where("participating = ?", true)
	}
	if f.RepositoryId != "" {
		q = q.Where("repository_id = ?", f.RepositoryId)
	}
	return q
}

func (p *notificationPers) List(userId string, f domain.NotificationFilter, offset, limit int) ([]domain.Notification, int64, error) {
	var total int64
	if err := p.filtered(userId, f).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []domain.Notification
	err := p.filtered(userId, f).Preload("Repository").Preload("Actor").Order("updated_at DESC").Offset(offset).Limit(limit).Find(&out).Error
	return out, total, err
}

func (p *notificationPers) UnreadCount(userId string) (int64, error) {
	var n int64
	err := p.db.Model(&domain.Notification{}).Where("user_id = ? AND unread = ?", userId, true).Count(&n).Error
	return n, err
}

func (p *notificationPers) Get(userId, id string) (*domain.Notification, error) {
	var n domain.Notification
	if err := p.db.Preload("Repository").Preload("Actor").Where("user_id = ? AND id = ?", userId, id).First(&n).Error; err != nil {
		return nil, err
	}
	return &n, nil
}

func (p *notificationPers) SetUnread(userId, id string, unread bool) error {
	res := p.db.Model(&domain.Notification{}).Where("user_id = ? AND id = ?", userId, id).Update("unread", unread)
	if res.Error == nil && res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return res.Error
}

func (p *notificationPers) MarkAllRead(userId, repositoryId string, before time.Time) error {
	q := p.db.Model(&domain.Notification{}).Where("user_id = ? AND unread = ? AND updated_at <= ?", userId, true, before)
	if repositoryId != "" {
		q = q.Where("repository_id = ?", repositoryId)
	}
	return q.Update("unread", false).Error
}

func (p *notificationPers) DeleteThread(threadType, threadId string) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("thread_type = ? AND thread_id = ?", threadType, threadId).Delete(&domain.Notification{}).Error; err != nil {
			return err
		}
		return tx.Where("thread_type = ? AND thread_id = ?", threadType, threadId).Delete(&domain.ThreadMute{}).Error
	})
}

func (p *notificationPers) Watch(userId, repositoryId string) (domain.WatchLevel, error) {
	var w domain.RepositoryWatch
	err := p.db.Where("user_id = ? AND repository_id = ?", userId, repositoryId).First(&w).Error
	if err == gorm.ErrRecordNotFound {
		return domain.WatchParticipating, nil
	}
	return w.Level, err
}

func (p *notificationPers) SetWatch(userId, repositoryId string, level domain.WatchLevel) error {
	if level == domain.WatchParticipating {
		return p.db.Where("user_id = ? AND repository_id = ?", userId, repositoryId).Delete(&domain.RepositoryWatch{}).Error
	}
	return p.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "repository_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"level", "updated_at"}),
	}).Create(&domain.RepositoryWatch{UserId: userId, RepositoryId: repositoryId, Level: level, UpdatedAt: time.Now()}).Error
}

func (p *notificationPers) Watchers(repositoryId string, levels ...domain.WatchLevel) ([]string, error) {
	var ids []string
	err := p.db.Model(&domain.RepositoryWatch{}).Where("repository_id = ? AND level IN ?", repositoryId, levels).Pluck("user_id", &ids).Error
	return ids, err
}

func (p *notificationPers) Ignoring(repositoryId string, userIds []string) (map[string]bool, error) {
	return p.among(p.db.Model(&domain.RepositoryWatch{}).Where("repository_id = ? AND level = ?", repositoryId, domain.WatchIgnore), userIds)
}

func (p *notificationPers) among(q *gorm.DB, userIds []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(userIds) == 0 {
		return out, nil
	}
	var ids []string
	err := q.Where("user_id IN ?", userIds).Pluck("user_id", &ids).Error
	for _, id := range ids {
		out[id] = true
	}
	return out, err
}

func (p *notificationPers) Mute(userId, threadType, threadId string) error {
	return p.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&domain.ThreadMute{UserId: userId, ThreadType: threadType, ThreadId: threadId}).Error
}

func (p *notificationPers) Unmute(userId, threadType, threadId string) error {
	return p.db.Where("user_id = ? AND thread_type = ? AND thread_id = ?", userId, threadType, threadId).Delete(&domain.ThreadMute{}).Error
}

func (p *notificationPers) Muted(threadType, threadId string, userIds []string) (map[string]bool, error) {
	return p.among(p.db.Model(&domain.ThreadMute{}).Where("thread_type = ? AND thread_id = ?", threadType, threadId), userIds)
}

func (p *notificationPers) IsMuted(userId, threadType, threadId string) (bool, error) {
	m, err := p.Muted(threadType, threadId, []string{userId})
	return m[userId], err
}

func (p *notificationPers) EmailLevel(userId string) (domain.EmailLevel, error) {
	var pref domain.NotificationPreference
	err := p.db.Where("user_id = ?", userId).First(&pref).Error
	if err == gorm.ErrRecordNotFound {
		return domain.EmailParticipating, nil
	}
	return pref.Email, err
}

func (p *notificationPers) SetEmailLevel(userId string, level domain.EmailLevel) error {
	return p.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"email"}),
	}).Create(&domain.NotificationPreference{UserId: userId, Email: level}).Error
}
