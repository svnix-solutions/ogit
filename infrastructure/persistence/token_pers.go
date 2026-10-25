package persistence

import (
	"time"

	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type tokenPers struct {
	db *gorm.DB
}

func NewTokenPers(db *gorm.DB) *tokenPers {
	return &tokenPers{db: db}
}

func (t *tokenPers) Create(token *domain.Token) error {
	return t.db.Create(token).Error
}

func (t *tokenPers) GetByNameAndToken(name, tokenStr string) (*domain.Token, error) {
	var token domain.Token
	err := t.db.Preload("User").Preload("Repository").Where("name = ? AND token = ?", name, tokenStr).First(&token).Error
	return &token, err
}

func (t *tokenPers) GetByToken(tokenStr string) (*domain.Token, error) {
	var token domain.Token
	err := t.db.Preload("User").Preload("Repository").Where("token = ?", tokenStr).First(&token).Error
	return &token, err
}

func (t *tokenPers) GetByRepositoryId(repositoryId string) ([]domain.Token, error) {
	var tokens []domain.Token
	err := t.db.Where("repository_id = ? AND type = ?", repositoryId, domain.TokenRepo).Order("created_at DESC").Find(&tokens).Error
	return tokens, err
}

func (t *tokenPers) Touch(id string, at time.Time) error {
	return t.db.Model(&domain.Token{}).Where("id = ?", id).Update("last_used_at", at).Error
}

func (t *tokenPers) GetByUserId(userId string) ([]domain.Token, error) {
	var tokens []domain.Token
	err := t.db.Preload("User").Preload("Repository").Where("user_id = ?", userId).Find(&tokens).Error
	return tokens, err
}

func (t *tokenPers) GetById(id string) (*domain.Token, error) {
	var token domain.Token
	err := t.db.Preload("User").Preload("Repository").Where("id = ?", id).First(&token).Error
	return &token, err
}

// Delete removes the token; callers check whose it is. (Filtering on
// user_id here deleted nothing for repository tokens, which have none.)
func (t *tokenPers) Delete(token *domain.Token) error {
	res := t.db.Where("id = ?", token.Id).Delete(&domain.Token{})
	if res.Error == nil && res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return res.Error
}
