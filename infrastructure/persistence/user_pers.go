package persistence

import (
	"strings"
	"time"

	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type userPers struct {
	db *gorm.DB
}

func NewUserPers(db *gorm.DB) *userPers {
	return &userPers{db: db}
}

func (u *userPers) GetById(id string) (domain.User, error) {
	var user domain.User
	err := u.db.Where("id = ?", id).First(&user).Error
	return user, err
}

func (u *userPers) GetByUsername(username string) (domain.User, error) {
	var user domain.User
	err := u.db.Where("username = ?", username).First(&user).Error
	return user, err
}

func (u *userPers) GetByEmail(email string) (domain.User, error) {
	var user domain.User
	err := u.db.Where("email = ?", email).First(&user).Error
	return user, err
}

func (u *userPers) MarkEmailVerified(userId string, at time.Time) error {
	return u.db.Model(&domain.User{}).Where("id = ?", userId).Update("email_verified_at", at).Error
}

func (u *userPers) List(query string, limit, offset int) ([]domain.User, int64, error) {
	q := u.db.Model(&domain.User{})
	if query != "" {
		like := "%" + strings.ToLower(query) + "%"
		q = q.Where("LOWER(username) LIKE ? OR LOWER(email) LIKE ?", like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var users []domain.User
	err := q.Order("username").Limit(limit).Offset(offset).Find(&users).Error
	return users, total, err
}

func (u *userPers) SetActive(userId string, active bool) error {
	return u.db.Model(&domain.User{}).Where("id = ?", userId).Update("active", active).Error
}

func (u *userPers) SetRole(userId string, role domain.Role) error {
	return u.db.Model(&domain.User{}).Where("id = ?", userId).Update("role", role).Error
}

func (u *userPers) SetPassword(userId, hash string) error {
	return u.db.Model(&domain.User{}).Where("id = ?", userId).Update("password", hash).Error
}

func (u *userPers) CountActiveAdmins() (int64, error) {
	var n int64
	err := u.db.Model(&domain.User{}).Where("role = ? AND active = ?", domain.RoleAdmin, true).Count(&n).Error
	return n, err
}

// Purge deletes dependent rows explicitly: sqlite does not enforce the
// foreign key cascades.
func (u *userPers) Purge(userId string) error {
	return u.db.Transaction(func(tx *gorm.DB) error {
		// Their pull requests stay, shown as by a deleted user.
		for _, column := range []string{"author_id", "merged_by_id"} {
			if err := tx.Model(&domain.PullRequest{}).Where(column+" = ?", userId).Update(column, nil).Error; err != nil {
				return err
			}
		}
		for _, q := range []struct {
			model any
			where string
		}{
			{&domain.Session{}, "user_id = ?"},
			{&domain.Token{}, "user_id = ?"},
			{&domain.EmailVerification{}, "user_id = ?"},
			{&domain.GroupMember{}, "user_id = ?"},
			{&domain.RepositoryMember{}, "user_id = ?"},
			{&domain.SSHKey{}, "user_id = ?"},
			{&domain.User{}, "id = ?"},
		} {
			if err := tx.Where(q.where, userId).Delete(q.model).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (u *userPers) Create(user *domain.User) error {
	return u.db.Create(user).Error
}
