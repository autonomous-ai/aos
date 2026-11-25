package otamirror

import (
	"reflect"
	"strings"
	"testing"
)

var (
	storage = Default.Storage
	cdn     = Default.CDN
)

func TestToCDN(t *testing.T) {
	cases := []struct{ in, want string }{
		{storage + "/os/ota/metadata.json", cdn + "/os/ota/metadata.json"},
		{storage + "/os/ota/hal/0.1.309.zip", cdn + "/os/ota/hal/0.1.309.zip"},
		{strings.Replace(storage, "storage.", "STORAGE.", 1) + "/os/ota/metadata.json?v=3", cdn + "/os/ota/metadata.json?v=3"},
		{strings.Replace(storage, "https:", "http:", 1) + "/os/software-update", cdn + "/os/software-update"},
		{" " + storage + "/os/skills/voice.zip\n", cdn + "/os/skills/voice.zip"},
		// Another bucket, a bucket whose name only starts the same, other hosts: unchanged.
		{"https://storage.googleapis.com/ota-test-bucket/os/ota/metadata.json", "https://storage.googleapis.com/ota-test-bucket/os/ota/metadata.json"},
		{storage + "x/os/a.zip", storage + "x/os/a.zip"},
		{"https://github.com/openai/codex/releases/download/x.tar.gz", "https://github.com/openai/codex/releases/download/x.tar.gz"},
		{cdn + "/os/ota/metadata.json", cdn + "/os/ota/metadata.json"},
		{strings.Replace(storage, "https://", "https://user@", 1) + "/os/a.zip", strings.Replace(storage, "https://", "https://user@", 1) + "/os/a.zip"},
		{"", ""},
		{"::not a url::", "::not a url::"},
	}
	for _, c := range cases {
		if got := ToCDN(c.in); got != c.want {
			t.Errorf("ToCDN(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCandidates(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{storage + "/os/ota/hal/0.1.309.zip", []string{cdn + "/os/ota/hal/0.1.309.zip", storage + "/os/ota/hal/0.1.309.zip"}},
		// A device already on the CDN still falls back to the bucket.
		{cdn + "/os/ota/metadata.json", []string{cdn + "/os/ota/metadata.json", storage + "/os/ota/metadata.json"}},
		{"https://github.com/autonomous-ai/picoclaw/releases/download/v1/picoclaw-linux-arm64", []string{"https://github.com/autonomous-ai/picoclaw/releases/download/v1/picoclaw-linux-arm64"}},
		{"https://storage.googleapis.com/ota-test-bucket/os/ota/metadata.json", []string{"https://storage.googleapis.com/ota-test-bucket/os/ota/metadata.json"}},
		{"  ", nil},
	}
	for _, c := range cases {
		if got := Candidates(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Candidates(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCustomMapping(t *testing.T) {
	m := Mapping{CDN: "http://127.0.0.1:1", Storage: "http://127.0.0.1:2/bucket"}
	got := m.Candidates("http://127.0.0.1:2/bucket/os/ota/metadata.json")
	want := []string{"http://127.0.0.1:1/os/ota/metadata.json", "http://127.0.0.1:2/bucket/os/ota/metadata.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Candidates = %q, want %q", got, want)
	}
	if got := m.ToStorage("http://127.0.0.1:1/os/x"); got != "http://127.0.0.1:2/bucket/os/x" {
		t.Fatalf("ToStorage = %q", got)
	}
}
