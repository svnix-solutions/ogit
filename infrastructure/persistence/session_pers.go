package persistence

import (
	"time"

	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type sessionPers struct {
	db *gorm.DB
}

func NewSessionPers(db *gorm.DB) *sessionPers {
	return &sessionPers{db: db}
}

func (s *sessionPers) Create(session *domain.Session) error {
	return s.db.Create(session).Error
}

func (s *sessionPers) DeleteById(id string) error {
	return s.db.Where("id = ?", id).Delete(&domain.Session{}).Error
}

func (s *sessionPers) DeleteByUserId(userId, keepId string) error {
	q := s.db.Where("user_id = ?", userId)
	if keepId != "" {
		q = q.Where("id <> ?", keepId)
	}
	return q.Delete(&domain.Session{}).Error
}

func (s *sessionPers) DeleteExpired(now time.Time) (int64, error) {
	res := s.db.Where("expires_at < ?", now).Delete(&domain.Session{})
	return res.RowsAffected, res.Error
}

func (s *sessionPers) GetById(id string) (*domain.Session, error) {
	var session domain.Session
	err := s.db.Where("id = ?", id).First(&session).Error
	return &session, err
}
