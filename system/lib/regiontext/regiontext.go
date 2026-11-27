// Package regiontext adapts agent instruction text to the device's market
// region. Outside China the text is returned untouched.
package regiontext

import "strings"

// ChinaConnectorsNote is added to the connectors paragraph in China so the
// agent says the services are unavailable instead of offering to set them up.
const ChinaConnectorsNote = "  - Google (Gmail, Calendar, Drive) and Facebook connectors are not available in this region: never offer or set them up, and if the user asks for one, say it is not available here."

// chinaReplacements rewrite the connector examples that name Google services.
// Each pair is exact text found in the runtimes' onboarding blocks; the
// package tests fail when a block still names Google after filtering.
var chinaReplacements = []struct{ old, new string }{
	{`"is my gmail connected?", "what's on my calendar", "list my events", "check my email", "send an email to …", "reply to that email"`, `"is my Notion connected?"`},
	{`"is my gmail connected?", "what's on my calendar", "list my events", "check my email"`, `"is my Notion connected?"`},
	{`"is my gmail connected?", "what's on my calendar", "send an email to …"`, `"is my Notion connected?"`},
	{"Gmail, Google Calendar, Google Drive, ", ""},
	{"the token connectors (Gmail/Calendar/Drive) have", "the token connectors have"},
	{"Gmail/Calendar/Drive are token-based and have", "Token-based connectors have"},
	{"mean their Google Calendar → use the `connectors` skill, not these files.", "are not answered from these files."},
}

// connectorsAnchor is the start of the first line that listed Google services;
// the China note goes at the end of that line's paragraph.
const connectorsAnchor = "Gmail, Google Calendar, Google Drive, "

// ForRegion returns block for the region: unchanged unless inChina, where
// Google services are dropped from the connector examples and a short note
// says they are unavailable.
func ForRegion(block string, inChina bool) string {
	if !inChina {
		return block
	}
	if i := strings.Index(block, connectorsAnchor); i >= 0 {
		// Insert the note after the paragraph's last bullet: the paragraph runs to
		// the first blank line, or the end of the text for a single-line block.
		end := len(block)
		if j := strings.Index(block[i:], "\n\n"); j >= 0 {
			end = i + j
		}
		block = block[:end] + "\n" + ChinaConnectorsNote + block[end:]
	}
	for _, r := range chinaReplacements {
		block = strings.ReplaceAll(block, r.old, r.new)
	}
	return block
}

// Leftover returns the Google terms still present in a China block outside the
// China note. Tests use it to catch new text that names a Google service.
func Leftover(block string) []string {
	text := strings.ReplaceAll(block, ChinaConnectorsNote, "")
	var found []string
	for _, term := range []string{"Gmail", "gmail", "Google", "what's on my calendar", "list my events", "check my email"} {
		if strings.Contains(text, term) {
			found = append(found, term)
		}
	}
	return found
}
