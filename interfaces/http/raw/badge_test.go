package raw

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBadgeEscapes(t *testing.T) {
	svg := badgeSVG(`<script>alert(1)</script>"`, "passing", "#2da44e")
	assert.NotContains(t, svg, "<script>")
	assert.Contains(t, svg, "&lt;script&gt;")
	assert.NotContains(t, svg, `"">`, "attributes stay closed")
}
