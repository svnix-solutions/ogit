package actions

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
)

type CacheEntry struct {
	Id         int64     `json:"id"`
	Key        string    `json:"key"`
	Ref        string    `json:"ref"`
	Size       int64     `json:"size"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
}

type CachesResponse struct {
	Caches    []CacheEntry `json:"caches"`
	TotalSize int64        `json:"total_size"`
	Limit     int64        `json:"limit"` // least recently used entries go beyond it
}

type CacheRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	CacheId    string `path:"cache_id" json:"-" validate:"required"`
}

func setupCaches(c Controller, repoGrp *fiberoapi.OApiGroup) {
	tags := []string{"Actions"}
	fiberoapi.Get(repoGrp, "/:identifier/actions/caches", c.Caches, fiberoapi.OpenAPIOptions{Summary: "List cache entries", OperationID: "actions.caches", Tags: tags,
		Description: "Maintainers and owners. Saved by actions/cache, most recently used first, with the repository's total and limit."})
	fiberoapi.Delete(repoGrp, "/:identifier/actions/caches/:cache_id", c.DeleteCache, fiberoapi.OpenAPIOptions{Summary: "Delete a cache entry", OperationID: "actions.cache.delete", Tags: tags,
		Description: "Maintainers and owners."})
}

func (c Controller) Caches(ctx *fiber.Ctx, req RepoRequest) (*CachesResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	entries, limit, err := c.Actions.ListCaches(id, path, user)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &CachesResponse{Caches: make([]CacheEntry, 0, len(entries)), Limit: limit}
	for _, e := range entries {
		resp.TotalSize += e.Size
		resp.Caches = append(resp.Caches, CacheEntry{Id: e.ApiId, Key: e.CacheKey, Ref: e.Scope, Size: e.Size, CreatedAt: e.CreatedAt, LastUsedAt: e.LastUsedAt})
	}
	return resp, nil
}

func (c Controller) DeleteCache(ctx *fiber.Ctx, req CacheRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	apiId, err := strconv.ParseInt(req.CacheId, 10, 64)
	if err != nil {
		return nil, &fiberoapi.ErrorResponse{Code: fiber.StatusNotFound, Details: "cache entry not found", Type: "CACHE_NOT_FOUND"}
	}
	id, path := repoid.Split(req.Identifier)
	if err := c.Actions.DeleteCache(id, path, user, apiId); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "cache entry deleted"}, nil
}
