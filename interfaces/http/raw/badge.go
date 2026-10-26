package raw

import (
	"fmt"
	"html"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/domain"
)

// Badge is /<owner>/<repo>/actions/workflows/<file>/badge.svg[?branch=]:
// the latest finished run of a workflow on a branch (default: the default
// branch), as an SVG (passing, failing, cancelled or no status).
func (c Controller) Badge(ctx *fiber.Ctx) error {
	repo, _, err := c.open(ctx)
	if err != nil {
		return c.fail(ctx, err)
	}
	label, message, color := ctx.Params("file"), "no status", "#9f9f9f"
	if c.Actions != nil {
		if run, err := c.Actions.LatestRun(repo, ctx.Params("file"), ctx.Query("branch")); err == nil {
			label = run.WorkflowName
			switch run.Status {
			case domain.RunSuccess:
				message, color = "passing", "#2da44e"
			case domain.RunFailure:
				message, color = "failing", "#cf222e"
			case domain.RunCancelled:
				message, color = "cancelled", "#9f9f9f"
			}
		}
	}
	ctx.Set(fiber.HeaderContentType, "image/svg+xml; charset=utf-8")
	ctx.Set(fiber.HeaderCacheControl, "no-cache, max-age=0")
	ctx.Set("X-Content-Type-Options", "nosniff")
	ctx.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	return ctx.SendString(badgeSVG(label, message, color))
}

// badgeSVG draws a flat two-part badge; text is escaped.
func badgeSVG(label, message, color string) string {
	if utf8.RuneCountInString(label) > 60 {
		label = string([]rune(label)[:60]) + "…"
	}
	w := func(s string) int { return 10 + 7*utf8.RuneCountInString(s) }
	lw, mw := w(label), w(message)
	esc := html.EscapeString
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="%s: %s">`, lw+mw, esc(label), esc(message))
	fmt.Fprintf(&b, `<title>%s: %s</title>`, esc(label), esc(message))
	fmt.Fprintf(&b, `<rect width="%d" height="20" fill="#555"/><rect x="%d" width="%d" height="20" fill="%s"/>`, lw, lw, mw, color)
	b.WriteString(`<g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" font-size="11">`)
	fmt.Fprintf(&b, `<text x="%d" y="14">%s</text><text x="%d" y="14">%s</text></g></svg>`, lw/2, esc(label), lw+mw/2, esc(message))
	return b.String()
}
