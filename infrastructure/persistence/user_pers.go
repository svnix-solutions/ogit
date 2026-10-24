package persistence

import (
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

func (u *userPers) Create(user *domain.User) error {
	return u.db.Create(user).Error
}
