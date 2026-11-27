package picoclaw

import (
	"strings"
	"testing"

	"go.autonomous.ai/os/system/lib/regiontext"
)

// In China the connector list and examples must not name Google services,
// and outside China the block must be exactly the shipped text.
func TestOnboardingBlocksForRegion(t *testing.T) {
	blocks := map[string]string{
		"agentsMDBlock": agentsMDBlock,
	}
	for name, block := range blocks {
		if got := regiontext.ForRegion(block, false); got != block {
			t.Errorf("%s: text changed outside China", name)
		}
		cn := regiontext.ForRegion(block, true)
		if left := regiontext.Leftover(cn); len(left) != 0 {
			t.Errorf("%s: China text still names %v", name, left)
		}
		if !strings.Contains(cn, regiontext.ChinaConnectorsNote) {
			t.Errorf("%s: China text is missing the unavailable-services note", name)
		}
		if !strings.Contains(cn, "Notion") {
			t.Errorf("%s: China text lost the other connectors", name)
		}
	}
}
