package application

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/labbs/ogit/domain"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"gorm.io/gorm"
)

// Protected tag rules (like GitHub's tag protection):
//   - repository owners manage the rules; developers and up can read them;
//   - a rule protects the tags matching its pattern (a name or a glob, as
//     for branches);
//   - only maintainers and up, and the users and groups the rule lists,
//     may create a matching tag; deploy keys and repository tokens never
//     may;
//   - nobody may move or delete a protected tag, except owners, and every
//     such bypass is logged ("component":"audit");
//   - checks run per reference on HTTP and SSH pushes (like branch
//     protection) and when publishing a release creates its tag.

var (
	ErrProtectedTag    = errors.New("protected tag")
	ErrTagRuleNotFound = errors.New("protected tag rule not found")
	ErrTagRuleExists   = errors.New("a protected tag rule with this pattern already exists")
	ErrInvalidTagRule  = errors.New("a rule needs a tag name or pattern (letters, digits, . _ - / @ +, and * ** ? wildcards)")
)

// ProtectedTagError says why a protected tag refused a change.
type ProtectedTagError struct {
	Tag    string
	Reason string
}

func (e *ProtectedTagError) Error() string {
	return fmt.Sprintf("tag %s is protected: %s", e.Tag, e.Reason)
}

func (e *ProtectedTagError) Unwrap() error { return ErrProtectedTag }

// TagRuleView is a protected tag rule with its allowed users and groups by
// name.
type TagRuleView struct {
	domain.ProtectedTag
	AllowedUsers  []string
	AllowedGroups []string
}

// TagRuleChange holds a rule's fields; nil ones are kept on update.
type TagRuleChange struct {
	Pattern       *string
	AllowedUsers  []string // usernames; nil keeps, empty clears
	AllowedGroups []string
}

func (c *ProtectionApp) ListTagRules(id, path *string, actorId string) ([]TagRuleView, error) {
	repo, err := c.repository(id, path, actorId, false)
	if err != nil {
		return nil, err
	}
	rules, err := c.Tags.List(repo.Id)
	if err != nil {
		return nil, err
	}
	out := make([]TagRuleView, 0, len(rules))
	for _, r := range rules {
		out = append(out, c.tagView(r))
	}
	return out, nil
}

func (c *ProtectionApp) GetTagRule(id, path *string, actorId, ruleId string) (*TagRuleView, error) {
	repo, err := c.repository(id, path, actorId, false)
	if err != nil {
		return nil, err
	}
	rule, err := c.tagRule(repo.Id, ruleId)
	if err != nil {
		return nil, err
	}
	v := c.tagView(*rule)
	return &v, nil
}

func (c *ProtectionApp) tagRule(repoId, ruleId string) (*domain.ProtectedTag, error) {
	rule, err := c.Tags.Get(repoId, ruleId)
	if isNotFound(err) {
		return nil, ErrTagRuleNotFound
	}
	return rule, err
}

func (c *ProtectionApp) tagView(r domain.ProtectedTag) TagRuleView {
	names := c.view(domain.BranchProtection{AllowedUserIds: r.AllowedUserIds, AllowedGroupIds: r.AllowedGroupIds})
	return TagRuleView{ProtectedTag: r, AllowedUsers: names.AllowedUsers, AllowedGroups: names.AllowedGroups}
}

func (c *ProtectionApp) CreateTagRule(id, path *string, actorId string, ch TagRuleChange) (*TagRuleView, error) {
	repo, err := c.repository(id, path, actorId, true)
	if err != nil {
		return nil, err
	}
	rule := &domain.ProtectedTag{RepositoryId: repo.Id}
	if _, err := c.applyTag(rule, ch); err != nil {
		return nil, err
	}
	if err := c.Tags.Create(rule); helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, ErrTagRuleExists
	} else if err != nil {
		return nil, err
	}
	c.audit(actorId, repo, "protected_tag.create").Str("pattern", rule.Pattern).Msg("protected tag rule created")
	v := c.tagView(*rule)
	return &v, nil
}

func (c *ProtectionApp) UpdateTagRule(id, path *string, actorId, ruleId string, ch TagRuleChange) (*TagRuleView, error) {
	repo, err := c.repository(id, path, actorId, true)
	if err != nil {
		return nil, err
	}
	rule, err := c.tagRule(repo.Id, ruleId)
	if err != nil {
		return nil, err
	}
	fields, err := c.applyTag(rule, ch)
	if err != nil {
		return nil, err
	}
	fields["updated_at"] = time.Now()
	if err := c.Tags.Update(rule.Id, fields); helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, ErrTagRuleExists
	} else if err != nil {
		return nil, err
	}
	c.audit(actorId, repo, "protected_tag.update").Str("pattern", rule.Pattern).Msg("protected tag rule changed")
	v := c.tagView(*rule)
	return &v, nil
}

func (c *ProtectionApp) DeleteTagRule(id, path *string, actorId, ruleId string) error {
	repo, err := c.repository(id, path, actorId, true)
	if err != nil {
		return err
	}
	rule, err := c.tagRule(repo.Id, ruleId)
	if err != nil {
		return err
	}
	if err := c.Tags.Delete(rule.Id); err != nil {
		return err
	}
	c.audit(actorId, repo, "protected_tag.delete").Str("pattern", rule.Pattern).Msg("protected tag rule deleted")
	return nil
}

// applyTag validates ch and applies it to rule, returning the changed
// columns. The user and group lists are resolved like branch protection's.
func (c *ProtectionApp) applyTag(rule *domain.ProtectedTag, ch TagRuleChange) (map[string]any, error) {
	fields := map[string]any{}
	if ch.Pattern != nil {
		rule.Pattern = strings.TrimPrefix(strings.TrimSpace(*ch.Pattern), "refs/tags/")
		fields["pattern"] = rule.Pattern
	}
	if !domain.ValidBranchPattern(rule.Pattern) {
		return nil, ErrInvalidTagRule
	}
	lists := domain.BranchProtection{Pattern: rule.Pattern}
	changed, err := c.apply(&lists, RuleChange{AllowedUsers: ch.AllowedUsers, AllowedGroups: ch.AllowedGroups})
	if err != nil {
		return nil, err
	}
	if ids, ok := changed["allowed_user_ids"]; ok {
		rule.AllowedUserIds, fields["allowed_user_ids"] = lists.AllowedUserIds, ids
	}
	if ids, ok := changed["allowed_group_ids"]; ok {
		rule.AllowedGroupIds, fields["allowed_group_ids"] = lists.AllowedGroupIds, ids
	}
	return fields, nil
}

// checkTag decides whether userId ("" for deploy keys and repository
// tokens) may change tag from old to new.
func (c *ProtectionApp) checkTag(repo *domain.Repository, rule *domain.ProtectedTag, userId, tag string, old, new plumbing.Hash) error {
	if rule == nil || old == new {
		return nil
	}
	if !domain.IsPerson(userId) {
		userId = ""
	}
	refuse := func(reason string) error { return &ProtectedTagError{Tag: tag, Reason: reason} }
	var violation error
	switch {
	case new.IsZero():
		violation = refuse("protected tags can't be deleted")
	case !old.IsZero():
		violation = refuse("protected tags can't be moved")
	default:
		return c.checkTagCreate(repo, rule, userId, tag)
	}
	if userId != "" {
		if role, err := c.Repositories.GetUserEffectiveRole(repo.Id, userId); err == nil && role == domain.AccessTypeOwner {
			c.audit(userId, repo, "protected_tag.bypass").Str("pattern", rule.Pattern).Str("tag", tag).Str("rule", violation.Error()).
				Msg("protected tag changed by an owner")
			return nil
		}
	}
	return violation
}

func (c *ProtectionApp) checkTagCreate(repo *domain.Repository, rule *domain.ProtectedTag, userId, tag string) error {
	if rule == nil {
		return nil
	}
	ok, err := c.mayCreateTag(repo, rule, userId)
	if err != nil {
		return err
	}
	if !ok {
		return &ProtectedTagError{Tag: tag, Reason: "only maintainers and the users and groups the rule lists may create it"}
	}
	return nil
}

// mayCreateTag: maintainers and up, or a user or group on the rule's list.
func (c *ProtectionApp) mayCreateTag(repo *domain.Repository, rule *domain.ProtectedTag, userId string) (bool, error) {
	if userId == "" {
		return false, nil
	}
	if role, err := c.Repositories.GetUserEffectiveRole(repo.Id, userId); err == nil && role.CanAdmin() {
		return true, nil
	}
	if slices.Contains(rule.AllowedUserIds, userId) {
		return true, nil
	}
	if len(rule.AllowedGroupIds) == 0 {
		return false, nil
	}
	// Members of an allowed group, inherited ones included.
	groups, err := groupRoles{groups: c.Groups}.effective(userId)
	if err != nil {
		return false, err
	}
	for g := range groups {
		if slices.Contains(rule.AllowedGroupIds, g) {
			return true, nil
		}
	}
	return false, nil
}

// ReleaseTagCheck decides whether caller may create tag by publishing a
// release (ReleaseApp.TagCheck).
func (c *ProtectionApp) ReleaseTagCheck(repo *domain.Repository, caller StatusCaller, tag string) error {
	if c == nil || c.Tags == nil {
		return nil
	}
	rules, err := c.Tags.List(repo.Id)
	if err != nil {
		return err
	}
	userId := ""
	if domain.IsPerson(caller.UserId) {
		userId = caller.UserId
	}
	return c.checkTagCreate(repo, domain.TagRuleFor(rules, tag), userId, tag)
}
