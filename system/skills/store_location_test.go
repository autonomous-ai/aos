package skills

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStoreLocationHeader(t *testing.T) {
	t.Cleanup(func() { StoreInChina = nil })
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("location")
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()
	t.Setenv("SKILL_STORE_BASE_URL", srv.URL)

	cases := []struct {
		name string
		hook func() bool
		want string
	}{
		{"unset", nil, "en-US"},
		{"outside china", func() bool { return false }, "en-US"},
		{"in china", func() bool { return true }, "zh-CN"},
	}
	for _, tc := range cases {
		StoreInChina = tc.hook
		if _, err := StoreGet("/api/v1/x", nil, time.Second, 1024); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: location = %q, want %q", tc.name, got, tc.want)
		}
	}
}
