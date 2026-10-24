package application

import (
	"errors"
	"strings"

	"github.com/labbs/ogit/domain"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"gorm.io/gorm"
)

// Namespace rules (see domain/namespace.go):
//   - a repository lives under a user (its creator's username by default) or
//     a group: <namespace>/<slug>, unique per namespace;
//   - users create repositories only in their own namespace; in a group's,
//     its maintainers and owner do;
//   - a group's members have their group role on the group's repositories
//     (member = developer), on top of each repository's own members;
//   - usernames and group names share one set of names.

var (
	ErrNamespaceNotFound = errors.New("no user or group with this name")
	ErrNamespaceTaken    = errors.New("this name is already used by a user or a group")
	// ErrNamespaceNotEmpty: a user or group that still owns repositories
	// can't be deleted; their paths would dangle.
	ErrNamespaceNotEmpty = errors.New("delete or move its repositories first")
)

// SplitPath splits "<namespace>/<slug>" (an optional .git suffix and
// surrounding slashes are ignored). ok is false for anything else, including
// the flat paths used before namespaces.
func SplitPath(path string) (namespace, slug string, ok bool) {
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	namespace, slug, ok = strings.Cut(path, "/")
	if !ok || namespace == "" || slug == "" || strings.Contains(slug, "/") {
		return "", "", false
	}
	return namespace, slug, true
}

// ResolveNamespace returns the user or group named path.
func (c *RepositoryApp) ResolveNamespace(path string) (*domain.Namespace, error) {
	return resolveNamespace(c.UserPers, c.GroupPers, path)
}

func resolveNamespace(users domain.UserPers, groups domain.GroupPers, path string) (*domain.Namespace, error) {
	if user, err := users.GetByUsername(path); err == nil {
		return &domain.Namespace{Type: domain.OwnerUser, Id: user.Id, Path: user.Username}, nil
	} else if !isNotFound(err) {
		return nil, err
	}
	if group, err := groups.GetByName(path); err == nil {
		return &domain.Namespace{Type: domain.OwnerGroup, Id: group.Id, Path: group.Name}, nil
	} else if !isNotFound(err) {
		return nil, err
	}
	return nil, ErrNamespaceNotFound
}

// checkNewNamespace validates a new username or group name and checks that
// no user or group already has it.
func checkNewNamespace(users domain.UserPers, groups domain.GroupPers, name string) error {
	if err := domain.ValidNamespaceName(name); err != nil {
		return err
	}
	if _, err := users.GetByUsername(name); err == nil {
		return ErrNamespaceTaken
	} else if !isNotFound(err) {
		return err
	}
	if _, err := groups.GetByName(name); err == nil {
		return ErrNamespaceTaken
	} else if !isNotFound(err) {
		return err
	}
	return nil
}

// namespaceRole is what owning the namespace gives userId on repo: owner of
// their own repositories, and their group role on a group's.
func (c *RepositoryApp) namespaceRole(repo *domain.Repository, userId string) (domain.AccessType, error) {
	switch repo.OwnerType {
	case domain.OwnerUser:
		if repo.OwnerId == userId {
			return domain.AccessTypeOwner, nil
		}
	case domain.OwnerGroup:
		m, err := c.GroupPers.GetMember(repo.OwnerId, userId)
		if isNotFound(err) {
			return domain.AccessTypeNone, nil
		} else if err != nil {
			return domain.AccessTypeNone, err
		}
		return groupRoleOnRepositories(m.Role), nil
	}
	return domain.AccessTypeNone, nil
}

func groupRoleOnRepositories(role domain.GroupRole) domain.AccessType {
	switch role {
	case domain.GroupRoleOwner:
		return domain.AccessTypeOwner
	case domain.GroupRoleMaintainer:
		return domain.AccessTypeMaintainer
	case domain.GroupRoleMember:
		return domain.AccessTypeDeveloper
	}
	return domain.AccessTypeNone
}

// namespaceForCreate resolves path and checks that actorId may create (or
// move) repositories there: their own namespace, or a group they maintain
// or own. Groups they are not in are reported as not found.
func (c *RepositoryApp) namespaceForCreate(path, actorId string) (*domain.Namespace, error) {
	ns, err := c.ResolveNamespace(path)
	if err != nil {
		return nil, err
	}
	switch ns.Type {
	case domain.OwnerUser:
		if ns.Id != actorId {
			return nil, ErrForbidden
		}
	case domain.OwnerGroup:
		m, err := c.GroupPers.GetMember(ns.Id, actorId)
		if isNotFound(err) {
			return nil, ErrNamespaceNotFound
		} else if err != nil {
			return nil, err
		}
		if m.Role != domain.GroupRoleOwner && m.Role != domain.GroupRoleMaintainer {
			return nil, ErrForbidden
		}
	}
	return ns, nil
}

// Transfer moves a repository the actor owns to another namespace they may
// create repositories in. The old path keeps working through a redirect,
// like after a rename. Members are kept; in a group's namespace the group's
// roles apply on top.
func (c *RepositoryApp) Transfer(id, path *string, actorId, target string) (*domain.Repository, error) {
	repo, role, err := c.repositoryForManager(id, path, actorId)
	if err != nil {
		return nil, err
	}
	if role != domain.AccessTypeOwner {
		return nil, ErrForbidden
	}
	ns, err := c.namespaceForCreate(target, actorId)
	if err != nil {
		return nil, err
	}
	return moveRepository(c.RepoPres, repo, ns)
}

func moveRepository(pers domain.RepositoryPers, repo *domain.Repository, ns *domain.Namespace) (*domain.Repository, error) {
	if ns.Type == repo.OwnerType && ns.Id == repo.OwnerId {
		return repo, nil
	}
	err := pers.Move(repo.Id, repo.Namespace, repo.Slug, *ns)
	if helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, ErrRepositoryNameTaken
	} else if err != nil {
		return nil, err
	}
	repo.Namespace, repo.OwnerType, repo.OwnerId = ns.Path, ns.Type, ns.Id
	return repo, nil
}

// NamespaceView is a namespace as userId may see it.
type NamespaceView struct {
	domain.Namespace
	Description  string // groups only
	Repositories []domain.Repository
	// CanCreate: userId may create repositories here.
	CanCreate bool
}

// ViewNamespace returns the repositories of namespace path that userId (""
// for anonymous) may read. A group is only reported to its members or when
// it has a repository the caller can see, so private groups stay hidden.
func (c *RepositoryApp) ViewNamespace(path, userId string) (*NamespaceView, error) {
	ns, err := c.ResolveNamespace(path)
	if err != nil {
		return nil, err
	}
	view := &NamespaceView{Namespace: *ns}
	repos, err := c.RepoPres.InNamespace(ns.Type, ns.Id)
	if err != nil {
		return nil, err
	}
	for _, r := range repos {
		ok := !r.IsPrivate
		if !ok && userId != "" {
			if ok, err = c.CanUserAccess(r.Id, userId, "read"); err != nil {
				return nil, err
			}
		}
		if ok {
			view.Repositories = append(view.Repositories, r)
		}
	}

	member := false
	if ns.Type == domain.OwnerGroup {
		group, err := c.GroupPers.GetById(ns.Id)
		if err != nil {
			return nil, err
		}
		view.Description = group.Description
		if userId != "" {
			m, err := c.GroupPers.GetMember(ns.Id, userId)
			if err != nil && !isNotFound(err) {
				return nil, err
			}
			member = err == nil
			view.CanCreate = member && (m.Role == domain.GroupRoleOwner || m.Role == domain.GroupRoleMaintainer)
		}
		if !member && len(view.Repositories) == 0 {
			return nil, ErrNamespaceNotFound
		}
	} else {
		view.CanCreate = userId != "" && ns.Id == userId
	}
	return view, nil
}
