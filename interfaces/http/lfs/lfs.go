// Package lfs serves the Git LFS API (batch, basic transfers, locks) under
// /<owner>/<repo>.git/info/lfs (rules in application/lfs.go).
package lfs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"github.com/labbs/ogit/interfaces/http/git"
	"github.com/rs/zerolog"
)

const mediaType = "application/vnd.git-lfs+json"

type Controller struct {
	Config config.Config
	Logger zerolog.Logger
	LFS    *application.LFSApp
	Limits *ratelimit.Guard
}

func Setup(c Controller, app *fiber.App) {
	const base = "/:owner/:repo/info/lfs"
	app.Post(base+"/objects/batch", c.Batch)
	app.Get(base+"/objects/:oid", c.Download)
	app.Put(base+"/objects/:oid", c.Upload)
	app.Post(base+"/objects/verify", c.Verify)
	app.Post(base+"/locks", c.CreateLock)
	app.Get(base+"/locks", c.ListLocks)
	app.Post(base+"/locks/verify", c.VerifyLocks)
	app.Post(base+"/locks/:id/unlock", c.Unlock)
}

func (c Controller) fail(ctx *fiber.Ctx, status int, msg string) error {
	return ctx.Status(status).JSON(fiber.Map{"message": msg}, mediaType)
}

func (c Controller) send(ctx *fiber.Ctx, status int, body any) error {
	return ctx.Status(status).JSON(body, mediaType)
}

// error maps application errors to answers.
func (c Controller) error(ctx *fiber.Ctx, err error) error {
	var conflict *application.LFSLockConflict
	switch {
	case errors.As(err, &conflict):
		return c.send(ctx, fiber.StatusConflict, fiber.Map{"lock": lockJSON(conflict.Lock), "message": "already locked by " + conflict.Lock.Owner.Username})
	case errors.Is(err, application.ErrGitUnauthenticated), errors.Is(err, application.ErrLFSBadToken):
		ctx.Set("LFS-Authenticate", `Basic realm="ogit"`)
		ctx.Set(fiber.HeaderWWWAuthenticate, `Basic realm="ogit"`)
		return c.fail(ctx, fiber.StatusUnauthorized, "authentication required")
	case errors.Is(err, application.ErrGitNotFound), errors.Is(err, application.ErrLFSNotFound), errors.Is(err, application.ErrLFSLockNotFound):
		return c.fail(ctx, fiber.StatusNotFound, err.Error())
	case errors.Is(err, application.ErrGitArchived):
		return c.fail(ctx, fiber.StatusForbidden, "repository is archived (read-only)")
	case errors.Is(err, application.ErrGitForbidden), errors.Is(err, application.ErrLFSLockOwner), errors.Is(err, application.ErrTwoFactorRequired):
		return c.fail(ctx, fiber.StatusForbidden, err.Error())
	case errors.Is(err, application.ErrLFSTooLarge):
		return c.fail(ctx, fiber.StatusRequestEntityTooLarge, err.Error())
	case errors.Is(err, application.ErrLFSInvalidObject), errors.Is(err, application.ErrLFSMismatch), errors.Is(err, application.ErrLFSInvalidPath):
		return c.fail(ctx, fiber.StatusUnprocessableEntity, err.Error())
	}
	c.Logger.Error().Err(err).Str("component", "http.lfs").Str("path", ctx.Path()).Msg("LFS request failed")
	return c.fail(ctx, fiber.StatusInternalServerError, "internal error")
}

type errLimited struct{ r ratelimit.Result }

func (errLimited) Error() string { return "too many requests" }

// authorize identifies the caller like git does, or with an LFS token from
// the batch API or git-lfs-authenticate. budget counts the request as a git
// operation (batch and locks; transfers are covered by their batch).
func (c Controller) authorize(ctx *fiber.Ctx, write, budget bool) (*application.LFSAccess, error) {
	path := ctx.Params("owner") + "/" + strings.TrimSuffix(ctx.Params("repo"), ".git")
	ip := c.Limits.ClientIP(ctx)
	header := ctx.Get(fiber.HeaderAuthorization)
	var acc *application.LFSAccess
	var err error
	rule, key := c.Limits.Rules.GitAnonymous, "git:ip:"+ip
	if token, ok := strings.CutPrefix(header, "Bearer "); ok {
		if r, blocked := c.Limits.LoginBlocked(ctx.UserContext(), ip, ""); blocked {
			return nil, errLimited{r}
		}
		acc, err = c.LFS.AuthorizeToken(path, token, write)
		if errors.Is(err, application.ErrLFSBadToken) {
			c.Limits.LoginFailed(ctx.UserContext(), ip, "")
		}
		rule, key = c.Limits.Rules.GitUser, "git:lfs:"+application.HashToken(token)[:16]
	} else if creds := git.BasicCredentials(header); creds != nil {
		if r, blocked := c.Limits.LoginBlocked(ctx.UserContext(), ip, ""); blocked {
			return nil, errLimited{r}
		}
		acc, err = c.LFS.AuthorizeBasic(path, creds, write)
		if errors.Is(err, application.ErrGitUnauthenticated) {
			c.Limits.LoginFailed(ctx.UserContext(), ip, "")
		}
		rule, key = c.Limits.Rules.GitUser, "git:token:"+application.HashToken(creds.Token)[:16]
	} else {
		acc, err = c.LFS.AuthorizeBasic(path, nil, write)
	}
	if err == nil && budget {
		if r := c.Limits.Allow(ctx.UserContext(), rule, key); !r.Allowed {
			return nil, errLimited{r}
		}
	}
	return acc, err
}

// authorized runs h for an authorized caller.
func (c Controller) authorized(ctx *fiber.Ctx, write, budget bool, h func(*application.LFSAccess) error) error {
	acc, err := c.authorize(ctx, write, budget)
	var limited errLimited
	if errors.As(err, &limited) {
		ctx.Set(fiber.HeaderRetryAfter, ratelimit.RetryAfter(limited.r))
		return c.fail(ctx, fiber.StatusTooManyRequests, "too many requests; try again later")
	}
	if err != nil {
		return c.error(ctx, err)
	}
	return h(acc)
}

func decode(ctx *fiber.Ctx, v any) bool {
	return json.Unmarshal(ctx.Body(), v) == nil
}

func (c Controller) baseURL(ctx *fiber.Ctx) string {
	if u := strings.TrimSuffix(c.Config.Server.PublicURL, "/"); u != "" {
		return u
	}
	return ctx.BaseURL()
}

type batchRequest struct {
	Operation string `json:"operation"`
	HashAlgo  string `json:"hash_algo"`
	Objects   []struct {
		Oid  string `json:"oid"`
		Size int64  `json:"size"`
	} `json:"objects"`
}

type action struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header,omitempty"`
	ExpiresIn int               `json:"expires_in,omitempty"`
}

func (c Controller) Batch(ctx *fiber.Ctx) error {
	var req batchRequest
	if !decode(ctx, &req) {
		return c.fail(ctx, fiber.StatusUnprocessableEntity, "invalid batch request")
	}
	if req.HashAlgo != "" && req.HashAlgo != "sha256" {
		return c.fail(ctx, fiber.StatusConflict, "only sha256 is supported")
	}
	if req.Operation != "download" && req.Operation != "upload" {
		return c.fail(ctx, fiber.StatusUnprocessableEntity, "operation must be download or upload")
	}
	return c.authorized(ctx, req.Operation == "upload", true, func(acc *application.LFSAccess) error {
		objects := make([]application.LFSObject, len(req.Objects))
		for i, o := range req.Objects {
			objects[i] = application.LFSObject{Oid: o.Oid, Size: o.Size}
		}
		results, err := c.LFS.Batch(acc, req.Operation, objects)
		if err != nil {
			return c.error(ctx, err)
		}
		token, ttl := c.LFS.Token(acc)
		auth := map[string]string{"Authorization": "Bearer " + token}
		href := c.baseURL(ctx) + "/" + acc.Repo.FullPath() + ".git/info/lfs/objects/"
		out := make([]fiber.Map, 0, len(results))
		for _, o := range results {
			obj := fiber.Map{"oid": o.Oid, "size": o.Size, "authenticated": true}
			switch {
			case o.Err != nil:
				code := fiber.StatusUnprocessableEntity
				if errors.Is(o.Err, application.ErrLFSNotFound) {
					code = fiber.StatusNotFound
				} else if !errors.Is(o.Err, application.ErrLFSInvalidObject) && !errors.Is(o.Err, application.ErrLFSTooLarge) {
					c.Logger.Error().Err(o.Err).Str("component", "http.lfs").Msg("batch object failed")
					code, o.Err = fiber.StatusInternalServerError, errors.New("internal error")
				}
				obj["error"] = fiber.Map{"code": code, "message": o.Err.Error()}
			case o.Need && req.Operation == "download":
				obj["actions"] = fiber.Map{"download": action{Href: href + o.Oid, Header: auth, ExpiresIn: int(ttl.Seconds())}}
			case o.Need:
				obj["actions"] = fiber.Map{
					"upload": action{Href: href + o.Oid, Header: auth, ExpiresIn: int(ttl.Seconds())},
					"verify": action{Href: href + "verify", Header: auth, ExpiresIn: int(ttl.Seconds())},
				}
			}
			out = append(out, obj)
		}
		return c.send(ctx, fiber.StatusOK, fiber.Map{"transfer": "basic", "objects": out, "hash_algo": "sha256"})
	})
}

func (c Controller) Download(ctx *fiber.Ctx) error {
	return c.authorized(ctx, false, false, func(acc *application.LFSAccess) error {
		r, size, err := c.LFS.Download(acc, ctx.Params("oid"))
		if err != nil {
			return c.error(ctx, err)
		}
		ctx.Set(fiber.HeaderContentType, "application/octet-stream")
		return ctx.SendStream(r, int(size))
	})
}

func (c Controller) Upload(ctx *fiber.Ctx) error {
	return c.authorized(ctx, true, false, func(acc *application.LFSAccess) error {
		body := io.Reader(ctx.Context().RequestBodyStream())
		if !ctx.Request().IsBodyStream() {
			body = bytes.NewReader(ctx.Body())
		}
		size := int64(ctx.Request().Header.ContentLength())
		if size < 0 {
			size = -1
		}
		if err := c.LFS.Upload(acc, ctx.Params("oid"), size, body); err != nil {
			if errors.Is(err, application.ErrLFSTooLarge) {
				// Clients read the answer once they have sent the body:
				// discard it (up to the limit again, never stored).
				_, _ = io.Copy(io.Discard, io.LimitReader(body, c.LFS.MaxObjectSize()+1))
			}
			return c.error(ctx, err)
		}
		return ctx.SendStatus(fiber.StatusOK)
	})
}

func (c Controller) Verify(ctx *fiber.Ctx) error {
	var req struct {
		Oid  string `json:"oid"`
		Size int64  `json:"size"`
	}
	if !decode(ctx, &req) {
		return c.fail(ctx, fiber.StatusUnprocessableEntity, "invalid verify request")
	}
	return c.authorized(ctx, true, false, func(acc *application.LFSAccess) error {
		if err := c.LFS.Verify(acc, req.Oid, req.Size); err != nil {
			return c.error(ctx, err)
		}
		return c.send(ctx, fiber.StatusOK, fiber.Map{})
	})
}

func lockJSON(l *domain.LFSLock) fiber.Map {
	return fiber.Map{"id": l.Id, "path": l.Path, "locked_at": l.CreatedAt.UTC().Format(time.RFC3339), "owner": fiber.Map{"name": l.Owner.Username}}
}

func locksJSON(locks []domain.LFSLock) []fiber.Map {
	out := make([]fiber.Map, 0, len(locks))
	for i := range locks {
		out = append(out, lockJSON(&locks[i]))
	}
	return out
}

func withCursor(m fiber.Map, next string) fiber.Map {
	if next != "" {
		m["next_cursor"] = next
	}
	return m
}

type refSpec struct {
	Name string `json:"name"`
}

func (c Controller) CreateLock(ctx *fiber.Ctx) error {
	var req struct {
		Path string  `json:"path"`
		Ref  refSpec `json:"ref"`
	}
	if !decode(ctx, &req) {
		return c.fail(ctx, fiber.StatusUnprocessableEntity, "invalid lock request")
	}
	return c.authorized(ctx, true, true, func(acc *application.LFSAccess) error {
		l, err := c.LFS.CreateLock(acc, req.Path, req.Ref.Name)
		if err != nil {
			return c.error(ctx, err)
		}
		return c.send(ctx, fiber.StatusCreated, fiber.Map{"lock": lockJSON(l)})
	})
}

func (c Controller) ListLocks(ctx *fiber.Ctx) error {
	return c.authorized(ctx, false, true, func(acc *application.LFSAccess) error {
		limit, _ := strconv.Atoi(ctx.Query("limit"))
		locks, next, err := c.LFS.ListLocks(acc, ctx.Query("path"), ctx.Query("id"), ctx.Query("cursor"), limit)
		if err != nil {
			return c.error(ctx, err)
		}
		return c.send(ctx, fiber.StatusOK, withCursor(fiber.Map{"locks": locksJSON(locks)}, next))
	})
}

func (c Controller) VerifyLocks(ctx *fiber.Ctx) error {
	var req struct {
		Cursor string  `json:"cursor"`
		Limit  int     `json:"limit"`
		Ref    refSpec `json:"ref"`
	}
	if len(ctx.Body()) > 0 && !decode(ctx, &req) {
		return c.fail(ctx, fiber.StatusUnprocessableEntity, "invalid verify request")
	}
	return c.authorized(ctx, true, true, func(acc *application.LFSAccess) error {
		ours, theirs, next, err := c.LFS.VerifyLocks(acc, req.Cursor, req.Limit)
		if err != nil {
			return c.error(ctx, err)
		}
		return c.send(ctx, fiber.StatusOK, withCursor(fiber.Map{"ours": locksJSON(ours), "theirs": locksJSON(theirs)}, next))
	})
}

func (c Controller) Unlock(ctx *fiber.Ctx) error {
	var req struct {
		Force bool    `json:"force"`
		Ref   refSpec `json:"ref"`
	}
	if len(ctx.Body()) > 0 && !decode(ctx, &req) {
		return c.fail(ctx, fiber.StatusUnprocessableEntity, "invalid unlock request")
	}
	return c.authorized(ctx, true, true, func(acc *application.LFSAccess) error {
		l, err := c.LFS.Unlock(acc, ctx.Params("id"), req.Force)
		if err != nil {
			return c.error(ctx, err)
		}
		return c.send(ctx, fiber.StatusOK, fiber.Map{"lock": lockJSON(l)})
	})
}
