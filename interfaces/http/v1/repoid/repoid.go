// Package repoid parses the :identifier of repository routes.
package repoid

import (
	"net/url"

	"github.com/labbs/ogit/infrastructure/helpers/validator"
)

// Split returns the repository id, or its path (<namespace>/<slug>). The
// path's slash must be URL-encoded (alice%2Fwebsite) so it stays one path
// segment; it is decoded here.
func Split(identifier string) (id, path *string) {
	if validator.IsValidUUID(identifier) {
		return &identifier, nil
	}
	if decoded, err := url.PathUnescape(identifier); err == nil {
		identifier = decoded
	}
	return nil, &identifier
}
