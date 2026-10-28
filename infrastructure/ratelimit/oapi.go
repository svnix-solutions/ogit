package ratelimit

import (
	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
)

// Refused is the fiber-oapi error for a rule that was hit; it also sets
// Retry-After.
func Refused(c *fiber.Ctx, r Result) *fiberoapi.ErrorResponse {
	c.Set(fiber.HeaderRetryAfter, RetryAfter(r))
	return &fiberoapi.ErrorResponse{
		Code:    fiber.StatusTooManyRequests,
		Details: "too many attempts; try again in " + RetryAfter(r) + " seconds",
		Type:    "RATE_LIMITED",
	}
}
