package domain

import "time"

type Session struct {
	Id     string
	UserId string

	User      User `gorm:"foreignKey:UserId;references:Id"`
	UserAgent string
	IpAddress string
	ExpiresAt time.Time
	// SudoUntil: sensitive actions need no new confirmation before then
	// (set at login and by POST /auth/sudo).
	SudoUntil *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (s *Session) TableName() string {
	return "session"
}

type SessionPers interface {
	Create(session *Session) error
	GetById(id string) (*Session, error)
	DeleteById(id string) error
	// DeleteByUserId ends all of a user's sessions except keepId (may be "").
	DeleteByUserId(userId, keepId string) error
	SetSudoUntil(id string, until time.Time) error
	// DeleteExpired removes sessions that expired before now and returns how many.
	DeleteExpired(now time.Time) (int64, error)
}
