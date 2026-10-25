package git

import (
	"bytes"
	"context"

	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	helpergit "github.com/labbs/ogit/infrastructure/helpers/git"
)

func (ctrl *Controller) InfoRefs(ctx *fiber.Ctx) error {
	logger := ctrl.Logger.With().Str("component", "http.git.inforefs").Logger()

	service := ctx.Query("service")
	if service != "git-upload-pack" && service != "git-receive-pack" {
		logger.Warn().Str("service", service).Msg("Invalid service parameter")
		return ctx.Status(fiber.StatusBadRequest).SendString("invalid service parameter")
	}

	repoPath, _ := ctx.Locals(storageKeyLocal).(string)
	if repoPath == "" {
		return ctx.SendStatus(fiber.StatusNotFound)
	}

	srv, ep, err := helpergit.GetTransportServer(repoPath, ctrl.StorageConfig)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get transport server")
		return ctx.Status(fiber.StatusInternalServerError).SendString("failed to get transport server")
	}

	ctx.Set("Cache-Control", "no-cache")
	switch service {
	case "git-upload-pack":
		ctx.Set("Content-Type", "application/x-git-upload-pack-advertisement")
		sess, err := srv.NewUploadPackSession(ep, nil)
		if err != nil {
			return ctx.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
		adv, err := sess.AdvertisedReferences()
		if err != nil {
			return ctx.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
		if err := helpergit.AdvertiseUploadPackHTTP(adv); err != nil {
			return ctx.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
		if err := helpergit.WriteServiceAdvertisement(ctx.Response().BodyWriter(), service); err != nil {
			return ctx.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
		if err := adv.Encode(ctx.Response().BodyWriter()); err != nil {
			return ctx.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
	case "git-receive-pack":
		ctx.Set("Content-Type", "application/x-git-receive-pack-advertisement")
		sess, err := srv.NewReceivePackSession(ep, nil)
		if err != nil {
			return ctx.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
		adv, err := sess.AdvertisedReferences()
		if err != nil {
			return ctx.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
		if err := helpergit.AdvertiseNoThin(adv); err != nil {
			return ctx.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
		if err := helpergit.WriteServiceAdvertisement(ctx.Response().BodyWriter(), service); err != nil {
			return ctx.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
		if err := adv.Encode(ctx.Response().BodyWriter()); err != nil {
			return ctx.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
	}
	return nil
}

func (ctrl *Controller) HandleUploadPack(c *fiber.Ctx) error {
	logger := ctrl.Logger.With().Str("component", "http.git.uploadpack").Logger()

	repoPath, _ := c.Locals(storageKeyLocal).(string)
	if repoPath == "" {
		return c.SendStatus(fiber.StatusNotFound)
	}

	srv, ep, err := helpergit.GetTransportServer(repoPath, ctrl.StorageConfig)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get transport server")
		return c.Status(fiber.StatusInternalServerError).SendString("failed to get transport server")
	}

	// Create an upload pack session
	sess, err := srv.NewUploadPackSession(ep, nil)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to create upload pack session")
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}

	objects, err := ctrl.StorageConfig.GetStorer(repoPath)
	if err != nil {
		return c.Status(fiber.StatusNotFound).SendString("repository not found")
	}

	c.Set("Content-Type", "application/x-git-upload-pack-result")
	if err := helpergit.ServeUploadPack(context.Background(), sess, objects, bytes.NewReader(c.Body()), c.Response().BodyWriter(), true); err != nil {
		logger.Error().Err(err).Msg("upload pack failed")
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}

	logger.Debug().Msg("Upload pack completed successfully")
	return nil
}

func (ctrl *Controller) HandleReceivePack(c *fiber.Ctx) error {
	logger := ctrl.Logger.With().Str("component", "http.git.receivepack").Logger()

	// Extract repository path from URL
	repoPath, _ := c.Locals(storageKeyLocal).(string)
	if repoPath == "" {
		return c.SendStatus(fiber.StatusNotFound)
	}

	logger.Debug().Str("repoPath", repoPath).Msg("Handling receive-pack request")

	// Get the go-git transport server for this repository
	_, ep, err := helpergit.GetTransportServer(repoPath, ctrl.StorageConfig)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get transport server")
		return err
	}
	st, err := ctrl.StorageConfig.GetStorer(repoPath)
	if err != nil {
		return c.Status(fiber.StatusNotFound).SendString("repository not found")
	}

	// Ref updates are checked against the old values in the request.
	// Protected branches are checked per reference, as go-git applies them.
	var check helpergit.RefCheck
	if repo, ok := c.Locals(repositoryLocal).(*domain.Repository); ok {
		pusher, _ := c.Locals(pusherLocal).(application.Pusher)
		check = application.LimitPush(pusher, ctrl.Protection.PushCheck(repo, pusher, st))
	}
	sess, expect, err := helpergit.GuardedReceivePack(st, ep, check)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to create receive pack session")
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}

	// Decode the reference update request from the client
	req := packp.NewReferenceUpdateRequest()
	if err := req.Decode(bytes.NewReader(c.Body())); err != nil {
		logger.Error().Err(err).Msg("Failed to decode receive pack request")
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}
	if helpergit.OnlyDeletes(req) {
		req.Packfile = nil
	}
	expect(req)

	// Process the receive pack request and generate a status report
	report, err := sess.ReceivePack(context.Background(), req)
	if repo, ok := c.Locals(repositoryLocal).(*domain.Repository); ok {
		pusher, _ := c.Locals(pusherLocal).(application.Pusher)
		applied := application.AppliedUpdates(req, report)
		ctrl.Webhooks.Pushed(repo, pusher, st, applied)
		ctrl.Pulls.SourcePushed(repo, pusher, applied)
	}
	c.Set("Content-Type", "application/x-git-receive-pack-result")
	if err != nil {
		logger.Warn().Err(err).Msg("Receive pack failed")
		// Even if there was an error, send the report: it says which refs failed.
		if report != nil {
			_ = report.Encode(c.Response().BodyWriter())
		}
		return nil
	}

	// Encode and send the status report back to the client
	if err := report.Encode(c.Response().BodyWriter()); err != nil {
		logger.Error().Err(err).Msg("Failed to encode receive pack report")
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}

	logger.Debug().Msg("Receive pack completed successfully")
	return nil
}
