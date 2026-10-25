package persistence

import (
	"fmt"

	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type assigneePers struct{ db *gorm.DB }

func NewAssigneePers(db *gorm.DB) *assigneePers { return &assigneePers{db: db} }

// table returns the assignee table and its item column for kind.
func assigneeTable(kind string) (string, string, error) {
	switch kind {
	case domain.AssignIssue:
		return "issue_assignee", "issue_id", nil
	case domain.AssignPullRequest:
		return "pull_request_assignee", "pull_request_id", nil
	}
	return "", "", fmt.Errorf("unknown assignment kind %q", kind)
}

func (p *assigneePers) Set(kind, itemId string, userIds []string) error {
	table, col, err := assigneeTable(kind)
	if err != nil {
		return err
	}
	return p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM "+table+" WHERE "+col+" = ?", itemId).Error; err != nil {
			return err
		}
		for _, u := range userIds {
			if err := tx.Exec("INSERT INTO "+table+" ("+col+", user_id) VALUES (?, ?)", itemId, u).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (p *assigneePers) For(kind string, itemIds []string) (map[string][]domain.User, error) {
	out := map[string][]domain.User{}
	table, col, err := assigneeTable(kind)
	if err != nil || len(itemIds) == 0 {
		return out, err
	}
	var rows []struct {
		ItemId string
		domain.User
	}
	err = p.db.Table(table).Select(table+"."+col+" AS item_id, \"user\".*").
		Joins("JOIN \"user\" ON \"user\".id = "+table+".user_id").
		Where(table+"."+col+" IN ?", itemIds).Order("\"user\".username").Scan(&rows).Error
	for _, r := range rows {
		out[r.ItemId] = append(out[r.ItemId], r.User)
	}
	return out, err
}
