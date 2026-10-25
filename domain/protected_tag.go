package domain

import "time"

// ProtectedTag protects the tags matching Pattern (a name or a glob, as
// for branches): only maintainers and up, and the users and groups listed,
// may create them; nobody may move or delete them, except owners (logged).
type ProtectedTag struct {
	Id              string `gorm:"primaryKey"`
	RepositoryId    string
	Pattern         string
	AllowedUserIds  StringList
	AllowedGroupIds StringList
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (ProtectedTag) TableName() string { return "protected_tag" }

type ProtectedTagPers interface {
	List(repositoryId string) ([]ProtectedTag, error)
	Get(repositoryId, id string) (*ProtectedTag, error)
	Create(t *ProtectedTag) error
	Update(id string, fields map[string]any) error
	Delete(id string) error
}

// TagRuleFor returns the rule protecting tag, or nil (the most specific
// one, as for branches).
func TagRuleFor(rules []ProtectedTag, tag string) *ProtectedTag {
	var best *ProtectedTag
	for i := range rules {
		r := &rules[i]
		if MatchBranch(r.Pattern, tag) && (best == nil || moreSpecific(r.Pattern, best.Pattern)) {
			best = r
		}
	}
	return best
}
