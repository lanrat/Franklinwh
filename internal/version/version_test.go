package version

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		cur, latest string
		want        bool
	}{
		{"v0.2.2", "v0.2.3", true},
		{"v0.2.9", "v0.10.0", true},
		{"v1.0.0", "v0.9.9", false},
		{"v0.2.2", "v0.2.2", false},
		{"dev", "v0.2.3", false},
		{"v0.2.2", "v0.3.0-rc1", false},
		{"v0.2.2", "garbage", false},
	} {
		if got := Newer(tc.cur, tc.latest); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.cur, tc.latest, got, tc.want)
		}
	}
}

func TestLatest(t *testing.T) {
	body := `{"tag_name":"v0.3.0","html_url":"https://github.com/lanrat/franklinwh/releases/tag/v0.3.0"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("no User-Agent")
		}
		io.WriteString(w, body)
	}))
	defer srv.Close()

	r, err := Latest(context.Background(), srv.URL)
	if err != nil || r.Version != "v0.3.0" || r.URL != "https://github.com/lanrat/franklinwh/releases/tag/v0.3.0" {
		t.Errorf("Latest = %+v, %v", r, err)
	}

	// A link anywhere else is replaced with the release's page.
	body = `{"tag_name":"v0.3.1","html_url":"javascript:alert(1)"}`
	if r, err := Latest(context.Background(), srv.URL); err != nil || r.URL != "https://github.com/lanrat/franklinwh/releases/tag/v0.3.1" {
		t.Errorf("Latest with bad URL = %+v, %v", r, err)
	}

	body = `{"tag_name":"nightly"}`
	if _, err := Latest(context.Background(), srv.URL); err == nil {
		t.Error("Latest accepted a non-version tag")
	}
}

func TestGetDefault(t *testing.T) {
	if Get() == "" {
		t.Error("Get() is empty")
	}
}
