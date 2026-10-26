package domain

import (
	"errors"
	"regexp"
	"strings"
)

// Namespaces: every repository lives under a user (their username) or a
// group (its name): /<namespace>/<repository>. Users and groups share one
// set of names, which must be usable as a URL path segment and must not
// shadow a page of the web UI or a server route.

var namespacePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// reservedNames are first path segments the server or the web UI use.
var reservedNames = map[string]bool{
	"admin": true, "api": true, "api-spec.json": true, "api-spec.yaml": true,
	"assets": true, "dashboard": true, "documentation": true, "explore": true,
	"favicon.svg": true, "groups": true, "help": true, "login": true,
	"logout": true, "new": true, "notifications": true, "register": true, "settings": true,
	"users": true, "verify": true,
}

var ErrInvalidNamespaceName = errors.New("name must be 1-64 characters: lowercase letters, digits, '.', '_' or '-', starting with a letter or digit, not ending in .git, and not a reserved word")

// ValidNamespaceName checks a new username or group name.
func ValidNamespaceName(name string) error {
	if !namespacePattern.MatchString(name) || strings.HasSuffix(name, ".git") || reservedNames[name] {
		return ErrInvalidNamespaceName
	}
	return nil
}

// Namespace is a resolved user or group.
type Namespace struct {
	Type OwnerType
	Id   string
	Path string
}
