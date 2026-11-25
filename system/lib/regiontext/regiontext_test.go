package regiontext

import (
	"strings"
	"testing"
)

const sample = `Intro.

**Connectors (MANDATORY):** Users link services (Gmail, Google Calendar, Google Drive, Notion, GitHub) — "is my gmail connected?", "what's on my calendar", "list my events", "check my email".
  - Gmail/Calendar/Drive are token-based and have NO MCP server.

Next paragraph.`

func TestForRegionOutsideChinaIsIdentity(t *testing.T) {
	if got := ForRegion(sample, false); got != sample {
		t.Fatalf("non-China text changed:\n%s", got)
	}
}

func TestForRegionChinaDropsGoogle(t *testing.T) {
	got := ForRegion(sample, true)
	if left := Leftover(got); len(left) != 0 {
		t.Fatalf("China text still names %v:\n%s", left, got)
	}
	if !strings.Contains(got, "(Notion, GitHub)") {
		t.Errorf("Notion/GitHub should stay in the list:\n%s", got)
	}
	if strings.Count(got, ChinaConnectorsNote) != 1 {
		t.Errorf("want exactly one China note:\n%s", got)
	}
	// The note ends the connectors paragraph, before the blank line.
	if !strings.Contains(got, ChinaConnectorsNote+"\n\nNext paragraph.") {
		t.Errorf("note not placed at the end of the paragraph:\n%s", got)
	}
}

func TestForRegionWithoutAnchorIsUnchanged(t *testing.T) {
	const plain = "No connector list here."
	if got := ForRegion(plain, true); got != plain {
		t.Fatalf("got %q", got)
	}
}
