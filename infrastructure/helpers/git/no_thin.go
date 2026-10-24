package git

import (
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
)

// NoThin is the receive-pack capability that forbids thin packs.
const NoThin capability.Capability = "no-thin"

// AdvertiseNoThin adds no-thin to a receive-pack advertisement. Without it
// git pushes thin packs, whose deltas reference objects already in the
// repository; go-git's receive-pack cannot resolve those ("reference delta
// not found"), so every push that git delta-compresses against existing
// history fails. go-git has no constant for it, but accepts the name.
func AdvertiseNoThin(adv *packp.AdvRefs) error {
	return adv.Capabilities.Add(NoThin)
}
