package application

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/domain"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

// Branch protection rules:
//   - repository owners manage the rules; developers and up can read them;
//   - a rule protects the branches matching its pattern (the most specific
//     rule wins, see domain.RuleFor). A protected branch refuses
//     force-pushes and deletion unless the rule allows them;
//   - require_pull_request refuses direct pushes to an existing branch: it
//     only moves by merging pull requests;
//   - allowed users and groups (when any are listed) are the only ones who
//     may push to the branch or merge into it; deploy keys and repository
//     tokens are never on that list;
//   - required_contexts must each be success (commit statuses) on a pull
//     request's head before it is merged into the branch;
//   - required_approvals (else the repository's) and
//     dismiss_stale_approvals (only approvals of the current head count)
//     apply to merging pull requests into the branch;
//   - with owners_bypass, owners may break the rule; every bypass is logged
//     ("component":"audit");
//   - protected tags (protected_tag.go) are checked in the same push check;
//   - checks run per reference on HTTP and SSH pushes (a refused reference
//     is reported to git, the others are applied) and on merges, including
//     deleting the source branch afterwards.

var (
	ErrProtectedBranch = errors.New("protected branch")
	ErrRuleNotFound    = errors.New("branch protection rule not found")
	ErrRuleExists      = errors.New("a rule with this pattern already exists")
	ErrInvalidRule     = errors.New("a rule needs a branch name or pattern (letters, digits, . _ - / @ +, and * ** ? wildcards), and required approvals between 0 and 10")
	ErrUnknownGroup    = errors.New("unknown group")
	ErrUnknownUser     = errors.New("unknown user")
)

// ProtectedError says why a protected branch refused a change.
type ProtectedError struct {
	Branch string
	Reason string
}

func (e *ProtectedError) Error() string {
	return fmt.Sprintf("branch %s is protected: %s", e.Branch, e.Reason)
}

func (e *ProtectedError) Unwrap() error { return ErrProtectedBranch }

type ProtectionApp struct {
	Logger       zerolog.Logger
	Repositories *RepositoryApp
	Rules        domain.BranchProtectionPers
	Tags         domain.ProtectedTagPers // protected_tag.go
	Users        domain.UserPers
	Groups       domain.GroupPers
}

// RuleView is a rule with its allowed users and groups by name.
type RuleView struct {
	domain.BranchProtection
	AllowedUsers  []string
	AllowedGroups []string
}

// RuleChange holds a rule's fields; nil fields are kept on update.
// RequiredApprovals -1 means the repository's.
type RuleChange struct {
	Pattern               *string
	AllowForcePush        *bool
	AllowDeletion         *bool
	RequirePullRequest    *bool
	RequiredApprovals     *int
	DismissStaleApprovals *bool
	AllowedUsers          []string // usernames; nil keeps, empty clears
	AllowedGroups         []string // group names
	RequiredContexts      []string // nil keeps, empty clears
	OwnersBypass          *bool
}

func (c *ProtectionApp) repository(id, path *string, actorId string, owner bool) (*domain.Repository, error) {
	repo, role, err := c.Repositories.repositoryForManager(id, path, actorId)
	if err != nil {
		return nil, err
	}
	if (owner && role != domain.AccessTypeOwner) || !role.CanWrite() {
		return nil, ErrForbidden
	}
	return repo, nil
}

func (c *ProtectionApp) List(id, path *string, actorId string) ([]RuleView, error) {
	repo, err := c.repository(id, path, actorId, false)
	if err != nil {
		return nil, err
	}
	rules, err := c.Rules.List(repo.Id)
	if err != nil {
		return nil, err
	}
	out := make([]RuleView, 0, len(rules))
	for _, r := range rules {
		out = append(out, c.view(r))
	}
	return out, nil
}

func (c *ProtectionApp) Get(id, path *string, actorId, ruleId string) (*RuleView, error) {
	repo, err := c.repository(id, path, actorId, false)
	if err != nil {
		return nil, err
	}
	rule, err := c.rule(repo.Id, ruleId)
	if err != nil {
		return nil, err
	}
	v := c.view(*rule)
	return &v, nil
}

func (c *ProtectionApp) rule(repoId, ruleId string) (*domain.BranchProtection, error) {
	rule, err := c.Rules.Get(repoId, ruleId)
	if isNotFound(err) {
		return nil, ErrRuleNotFound
	}
	return rule, err
}

// view resolves the allowed ids to names, skipping deleted users and groups.
func (c *ProtectionApp) view(r domain.BranchProtection) RuleView {
	v := RuleView{BranchProtection: r, AllowedUsers: []string{}, AllowedGroups: []string{}}
	for _, id := range r.AllowedUserIds {
		if u, err := c.Users.GetById(id); err == nil {
			v.AllowedUsers = append(v.AllowedUsers, u.Username)
		}
	}
	for _, id := range r.AllowedGroupIds {
		if g, err := c.Groups.GetById(id); err == nil {
			v.AllowedGroups = append(v.AllowedGroups, g.Name)
		}
	}
	return v
}

func (c *ProtectionApp) Create(id, path *string, actorId string, ch RuleChange) (*RuleView, error) {
	repo, err := c.repository(id, path, actorId, true)
	if err != nil {
		return nil, err
	}
	rule := &domain.BranchProtection{RepositoryId: repo.Id}
	if _, err := c.apply(rule, ch); err != nil {
		return nil, err
	}
	if err := c.Rules.Create(rule); helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, ErrRuleExists
	} else if err != nil {
		return nil, err
	}
	c.audit(actorId, repo, "branch_protection.create").Str("pattern", rule.Pattern).Msg("branch protection rule created")
	v := c.view(*rule)
	return &v, nil
}

func (c *ProtectionApp) Update(id, path *string, actorId, ruleId string, ch RuleChange) (*RuleView, error) {
	repo, err := c.repository(id, path, actorId, true)
	if err != nil {
		return nil, err
	}
	rule, err := c.rule(repo.Id, ruleId)
	if err != nil {
		return nil, err
	}
	fields, err := c.apply(rule, ch)
	if err != nil {
		return nil, err
	}
	fields["updated_at"] = time.Now()
	if err := c.Rules.Update(rule.Id, fields); helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, ErrRuleExists
	} else if err != nil {
		return nil, err
	}
	c.audit(actorId, repo, "branch_protection.update").Str("pattern", rule.Pattern).Msg("branch protection rule changed")
	v := c.view(*rule)
	return &v, nil
}

func (c *ProtectionApp) Delete(id, path *string, actorId, ruleId string) error {
	repo, err := c.repository(id, path, actorId, true)
	if err != nil {
		return err
	}
	rule, err := c.rule(repo.Id, ruleId)
	if err != nil {
		return err
	}
	if err := c.Rules.Delete(rule.Id); err != nil {
		return err
	}
	c.audit(actorId, repo, "branch_protection.delete").Str("pattern", rule.Pattern).Msg("branch protection rule deleted")
	return nil
}

// apply validates ch and applies it to rule, returning the changed columns.
func (c *ProtectionApp) apply(rule *domain.BranchProtection, ch RuleChange) (map[string]any, error) {
	fields := map[string]any{}
	if ch.Pattern != nil {
		rule.Pattern = strings.TrimPrefix(strings.TrimSpace(*ch.Pattern), "refs/heads/")
		fields["pattern"] = rule.Pattern
	}
	if !domain.ValidBranchPattern(rule.Pattern) {
		return nil, ErrInvalidRule
	}
	for col, pair := range map[string]struct {
		dst *bool
		src *bool
	}{
		"allow_force_push": {&rule.AllowForcePush, ch.AllowForcePush}, "allow_deletion": {&rule.AllowDeletion, ch.AllowDeletion},
		"require_pull_request": {&rule.RequirePullRequest, ch.RequirePullRequest}, "dismiss_stale_approvals": {&rule.DismissStaleApprovals, ch.DismissStaleApprovals},
		"owners_bypass": {&rule.OwnersBypass, ch.OwnersBypass},
	} {
		if pair.src != nil {
			*pair.dst = *pair.src
			fields[col] = *pair.src
		}
	}
	if ch.RequiredApprovals != nil {
		switch n := *ch.RequiredApprovals; {
		case n == -1:
			rule.RequiredApprovals = nil
		case n >= 0 && n <= 10:
			rule.RequiredApprovals = &n
		default:
			return nil, ErrInvalidRule
		}
		fields["required_approvals"] = rule.RequiredApprovals
	}
	if ch.AllowedUsers != nil {
		ids := domain.StringList{}
		for _, name := range ch.AllowedUsers {
			u, err := c.Users.GetByUsername(strings.TrimSpace(name))
			if err != nil {
				return nil, fmt.Errorf("%w: %s", ErrUnknownUser, name)
			}
			if !slices.Contains(ids, u.Id) {
				ids = append(ids, u.Id)
			}
		}
		rule.AllowedUserIds, fields["allowed_user_ids"] = ids, ids
	}
	if ch.AllowedGroups != nil {
		ids := domain.StringList{}
		for _, name := range ch.AllowedGroups {
			g, err := c.Groups.GetByName(strings.TrimSpace(name))
			if err != nil {
				return nil, fmt.Errorf("%w: %s", ErrUnknownGroup, name)
			}
			if !slices.Contains(ids, g.Id) {
				ids = append(ids, g.Id)
			}
		}
		rule.AllowedGroupIds, fields["allowed_group_ids"] = ids, ids
	}
	if ch.RequiredContexts != nil {
		contexts := domain.StringList{}
		for _, ctx := range ch.RequiredContexts {
			ctx = strings.TrimSpace(ctx)
			if ctx == "" || len(ctx) > 255 || len(contexts) >= 20 {
				return nil, ErrInvalidRule
			}
			if !slices.Contains(contexts, ctx) {
				contexts = append(contexts, ctx)
			}
		}
		rule.RequiredContexts, fields["required_contexts"] = contexts, contexts
	}
	return fields, nil
}

func (c *ProtectionApp) audit(actorId string, repo *domain.Repository, action string) *zerolog.Event {
	return c.Logger.Info().Str("component", "audit").Str("actor_id", actorId).Str("action", action).Str("repository", repo.FullPath())
}

// RuleFor returns the rule protecting branch in repo, or nil.
func (c *ProtectionApp) RuleFor(repo *domain.Repository, branch string) (*domain.BranchProtection, error) {
	if c == nil {
		return nil, nil
	}
	rules, err := c.Rules.List(repo.Id)
	if err != nil {
		return nil, err
	}
	return domain.RuleFor(rules, branch), nil
}

// allowed reports whether userId is on the rule's list (any user with
// write access when the rule lists nobody).
func (c *ProtectionApp) allowed(rule *domain.BranchProtection, userId string) (bool, error) {
	if !rule.Restricted() {
		return true, nil
	}
	if userId == "" {
		return false, nil
	}
	if slices.Contains(rule.AllowedUserIds, userId) {
		return true, nil
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

// bypass reports whether userId may break rule (owners, when the rule lets
// them), and logs it when they do.
func (c *ProtectionApp) bypass(repo *domain.Repository, rule *domain.BranchProtection, userId, what string, violation error) bool {
	if !rule.OwnersBypass || userId == "" {
		return false
	}
	role, err := c.Repositories.GetUserEffectiveRole(repo.Id, userId)
	if err != nil || role != domain.AccessTypeOwner {
		return false
	}
	c.audit(userId, repo, "branch_protection.bypass").Str("pattern", rule.Pattern).Str("operation", what).Str("rule", violation.Error()).
		Msg("branch protection bypassed by an owner")
	return true
}

// checkRef decides whether pusher may move branch from old to new
// directly (not by a merge). objects must hold new.
func (c *ProtectionApp) checkRef(repo *domain.Repository, rule *domain.BranchProtection, pusher Pusher, objects storer.EncodedObjectStorer, branch string, old, new plumbing.Hash) error {
	if rule == nil {
		return nil
	}
	refuse := func(reason string) error { return &ProtectedError{Branch: branch, Reason: reason} }
	var violation error
	switch {
	case new.IsZero() && !rule.AllowDeletion:
		violation = refuse("deleting it is not allowed")
	case !old.IsZero() && !new.IsZero() && rule.RequirePullRequest:
		violation = refuse("changes must be merged through a pull request")
	}
	if violation == nil {
		ok, err := c.allowed(rule, pusher.UserId)
		if err != nil {
			return err
		}
		if !ok {
			violation = refuse("you are not allowed to push to it")
		}
	}
	if violation == nil && !old.IsZero() && !new.IsZero() && !rule.AllowForcePush && !isAncestor(objects, old, new) {
		violation = refuse("force-pushes are not allowed")
	}
	if violation == nil || c.bypass(repo, rule, pusher.UserId, "push", violation) {
		return nil
	}
	return violation
}

// PushCheck returns the check for a push to repo by pusher (nil: no
// rules). Rules are read once per push.
func (c *ProtectionApp) PushCheck(repo *domain.Repository, pusher Pusher, objects storer.EncodedObjectStorer) func(plumbing.ReferenceName, plumbing.Hash, plumbing.Hash) error {
	if c == nil {
		return nil
	}
	var rules []domain.BranchProtection
	var tags []domain.ProtectedTag
	var loadErr error
	loaded, tagsLoaded := false, false
	return func(name plumbing.ReferenceName, old, new plumbing.Hash) error {
		if name.IsTag() && c.Tags != nil {
			if !tagsLoaded {
				tags, loadErr = c.Tags.List(repo.Id)
				tagsLoaded = true
			}
			if loadErr != nil {
				return loadErr
			}
			tag := name.Short()
			return c.checkTag(repo, domain.TagRuleFor(tags, tag), pusher.UserId, tag, old, new)
		}
		if !name.IsBranch() {
			return nil
		}
		if !loaded {
			rules, loadErr = c.Rules.List(repo.Id)
			loaded = true
		}
		if loadErr != nil {
			return loadErr
		}
		branch := name.Short()
		return c.checkRef(repo, domain.RuleFor(rules, branch), pusher, objects, branch, old, new)
	}
}

// CheckDelete decides whether actorId may delete branch (at tip) after a
// merge.
func (c *ProtectionApp) CheckDelete(repo *domain.Repository, actorId, branch string, tip plumbing.Hash) error {
	rule, err := c.RuleFor(repo, branch)
	if err != nil || rule == nil {
		return err
	}
	return c.checkRef(repo, rule, Pusher{UserId: actorId}, nil, branch, tip, plumbing.ZeroHash)
}
