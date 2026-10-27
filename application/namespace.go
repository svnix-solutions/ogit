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
	// ErrNamespaceHasPackages: a group that still owns packages.
	ErrNamespaceHasPackages = errors.New("delete its packages first")
)

// SplitPath splits "<namespace>/<slug>", where the namespace may be a
// sub-group's path (org/team/app); an optional .git suffix and surrounding
// slashes are ignored. ok is false for anything else, including the flat
// paths used before namespaces.
func SplitPath(path string) (namespace, slug string, ok bool) {
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	i := strings.LastIndex(path, "/")
	if i <= 0 || i == len(path)-1 || strings.Count(path, "/") > domain.MaxGroupDepth {
		return "", "", false
	}
	namespace, slug = path[:i], path[i+1:]
	for _, part := range strings.Split(namespace, "/") {
		if part == "" {
			return "", "", false
		}
	}
	return namespace, slug, true
}

// groupAtPath refuses a repository path that is a sub-group's: org/team
// can't be both (they would share a URL).
func groupAtPath(groups domain.GroupPers, namespace, slug string) error {
	if _, err := groups.GetByName(namespace + "/" + slug); err == nil {
		return ErrRepositoryNameTaken
	} else if !isNotFound(err) {
		return err
	}
	return nil
}

// ResolveNamespace returns the user or group named path.
func (c *RepositoryApp) ResolveNamespace(path string) (*domain.Namespace, error) {
	return resolveNamespace(c.UserPers, c.GroupPers, path)
}

func resolveNamespace(users domain.UserPers, groups domain.GroupPers, path string) (*domain.Namespace, error) {
	if !strings.Contains(path, "/") { // sub-groups' paths have slashes, usernames never
		if user, err := users.GetByUsername(path); err == nil {
			return &domain.Namespace{Type: domain.OwnerUser, Id: user.Id, Path: user.Username}, nil
		} else if !isNotFound(err) {
			return nil, err
		}
	}
	if group, err := groups.GetByName(path); err == nil {
		return &domain.Namespace{Type: domain.OwnerGroup, Id: group.Id, Path: group.Name}, nil
	} else if !isNotFound(err) {
		return nil, err
	}
	// A sub-group renamed or moved: its former path.
	if group, err := groups.ResolveRedirect(path); err == nil {
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
		role, err := c.groupRole(repo.OwnerId, userId)
		if isNotFound(err) {
			return domain.AccessTypeNone, nil
		} else if err != nil {
			return domain.AccessTypeNone, err
		}
		return groupRoleOnRepositories(role), nil
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
		group, err := c.GroupPers.GetById(ns.Id)
		if err != nil {
			return nil, err
		}
		// Membership first (else: not found), then whether it counts.
		role, _, err := groupRoles{groups: c.GroupPers}.role(group, actorId)
		if err != nil {
			return nil, err
		}
		if role == "" {
			return nil, ErrNamespaceNotFound
		}
		if role.Level() < domain.GroupRoleMaintainer.Level() {
			return nil, ErrForbidden
		}
		if usable, _, err := c.groupRoles().role(group, actorId); err != nil {
			return nil, err
		} else if usable.Level() < domain.GroupRoleMaintainer.Level() {
			return nil, ErrGroupRequiresTwoFactor
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
	from := HookOwner{Login: repo.Namespace, Id: repo.OwnerId, Type: hookOwnerType(repo.OwnerType)}
	moved, err := moveRepository(c.RepoPres, c.GroupPers, repo, ns)
	if err == nil && moved.Namespace != from.Login {
		c.Webhooks.RepositoryEvent(moved, "transferred", actorId, map[string]any{"owner": map[string]any{"from": map[string]any{"user": from}}})
	}
	return moved, err
}

func moveRepository(pers domain.RepositoryPers, groups domain.GroupPers, repo *domain.Repository, ns *domain.Namespace) (*domain.Repository, error) {
	if ns.Type == repo.OwnerType && ns.Id == repo.OwnerId {
		return repo, nil
	}
	if err := groupAtPath(groups, ns.Path, repo.Slug); err != nil {
		return nil, err
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
	// Subgroups: the group's sub-groups userId may see.
	Subgroups []domain.Group
	// CanCreate: userId may create repositories (and sub-groups) here.
	CanCreate bool
}

// groupVisible reports whether userId may see a group: they're a member
// (direct or inherited), or they can read a repository in it or below.
func (c *RepositoryApp) groupVisible(group *domain.Group, userId string, member bool) (bool, error) {
	if member {
		return true, nil
	}
	if role, _, err := (groupRoles{groups: c.GroupPers}).role(group, userId); err != nil || role != "" {
		return role != "", err
	}
	groups := []domain.Group{*group}
	below, err := c.GroupPers.Descendants(group.Name)
	if err != nil {
		return false, err
	}
	groups = append(groups, below...)
	for _, g := range groups {
		repos, err := c.RepoPres.InNamespace(domain.OwnerGroup, g.Id)
		if err != nil {
			return false, err
		}
		for _, r := range repos {
			if !r.IsPrivate {
				return true, nil
			}
			if userId != "" {
				if ok, err := c.CanUserAccess(r.Id, userId, "read"); err != nil || ok {
					return ok, err
				}
			}
		}
	}
	return false, nil
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
		role, _, err := groupRoles{groups: c.GroupPers}.role(group, userId)
		if err != nil {
			return nil, err
		}
		member = role != ""
		view.CanCreate = role.Level() >= domain.GroupRoleMaintainer.Level()
		children, err := c.GroupPers.Children(group.Id)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			// Members see every sub-group (they're members of them too);
			// others those with something they can read.
			if ok, err := c.groupVisible(&child, userId, member); err != nil {
				return nil, err
			} else if ok {
				view.Subgroups = append(view.Subgroups, child)
			}
		}
		if !member && len(view.Repositories) == 0 {
			if ok, err := c.groupVisible(group, userId, false); err != nil || !ok {
				return nil, errOr(err, ErrNamespaceNotFound)
			}
		}
	} else {
		view.CanCreate = userId != "" && ns.Id == userId
	}
	return view, nil
}
