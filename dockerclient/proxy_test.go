package dockerclient

import (
	"net/http"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A socket proxy such as tecnativa/docker-socket-proxy allows each part of
// the API with a setting named for the path's first segment, and every
// method but GET and HEAD with POST. The hint names the settings that allow
// the refused request.
func TestProxySettingsNameWhatAllowsTheRequest(t *testing.T) {
	cases := []struct {
		method, path string
		want         []string
	}{
		{http.MethodGet, "/images/mongo:8/json", []string{"IMAGES"}},
		{http.MethodGet, "/v1.44/version", []string{"VERSION"}},
		{http.MethodHead, "/_ping", []string{"PING"}},
		{http.MethodPost, "/containers/create", []string{"CONTAINERS", "POST"}},
		{http.MethodPut, "/containers/abc/archive", []string{"CONTAINERS", "POST"}},
		{http.MethodPost, "/exec/abc/start", []string{"EXEC", "POST"}},
		{http.MethodDelete, "/v1.44/containers/abc", []string{"CONTAINERS", "POST"}},
		{http.MethodGet, "/", nil},
		{http.MethodPost, "/", []string{"POST"}},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, proxySettings(tc.method, tc.path), "%s %s", tc.method, tc.path)
	}
}

// The path comes from the request, which may carry caller input, so naming
// a setting from it must never panic and must only ever name a setting.
func FuzzProxySettings(f *testing.F) {
	for _, seed := range []string{"/images/mongo:8/json", "/v1.44/containers/create", "/_ping", "", "/", "//", "/v1.44", "/ü/x", "/a b/c"} {
		f.Add(http.MethodGet, seed)
		f.Add(http.MethodPost, seed)
	}
	f.Fuzz(func(t *testing.T, method, path string) {
		settings := proxySettings(method, path)
		if len(settings) > 2 {
			t.Fatalf("proxySettings(%q, %q) named %d settings; a request needs at most its section and POST", method, path, len(settings))
		}
		// A method that writes adds POST to what reading the path needs.
		want := proxySettings(http.MethodGet, path)
		if method != http.MethodGet && method != http.MethodHead {
			want = append(want, "POST")
		}
		if !slices.Equal(settings, want) {
			t.Fatalf("proxySettings(%q, %q) = %q, want %q: POST must be added exactly when the method is not GET or HEAD", method, path, settings, want)
		}
		for _, setting := range settings {
			for _, r := range setting {
				if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
					t.Fatalf("proxySettings(%q, %q) named %q, which is not a setting name", method, path, setting)
				}
			}
		}
	})
}
