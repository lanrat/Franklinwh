package gui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func startServer(t *testing.T, cfg Config) (*Server, string) {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, u
}

func get(t *testing.T, u string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", u, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestAccessRequiresToken(t *testing.T) {
	s, pageURL := startServer(t, Config{})
	u, _ := url.Parse(pageURL)
	tok := u.Query().Get("t")
	if tok == "" || tok != s.uiToken {
		t.Fatalf("URL %q lacks the UI token", pageURL)
	}
	base := "http://" + u.Host

	if code, body := get(t, pageURL, nil); code != 200 || !strings.Contains(body, tok) {
		t.Errorf("page with token: %d", code)
	}
	for _, p := range []string{"/", "/?t=wrong"} {
		if code, body := get(t, base+p, nil); code != 403 || strings.Contains(body, tok) {
			t.Errorf("GET %s: %d, leaked token %v", p, code, strings.Contains(body, tok))
		}
	}
	if code, _ := get(t, base+"/api/session", nil); code != 403 {
		t.Errorf("API without token: %d", code)
	}
	if code, body := get(t, base+"/api/session", map[string]string{"X-UI-Token": tok}); code != 200 || !strings.Contains(body, `"loggedIn":false`) {
		t.Errorf("API with token: %d %s", code, body)
	}
}

func TestEmbedded(t *testing.T) {
	for _, embedded := range []bool{false, true} {
		_, pageURL := startServer(t, Config{Embedded: embedded, Email: "a@b.c"})
		_, body := get(t, pageURL, nil)
		want := `<meta name="embedded" content="0">`
		if embedded {
			want = `<meta name="embedded" content="1">`
		}
		if !strings.Contains(body, want) {
			t.Errorf("embedded=%v: page lacks %s", embedded, want)
		}
	}
}

func TestVersionAndUpdateCheck(t *testing.T) {
	var hits atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, `{"tag_name":"v0.3.0","html_url":"https://github.com/lanrat/franklinwh/releases/tag/v0.3.0"}`)
	}))
	defer gh.Close()

	versionInfo := func(cfg Config) map[string]any {
		s, pageURL := startServer(t, cfg)
		u, _ := url.Parse(pageURL)
		var got map[string]any
		for i := 0; i < 50; i++ { // the check runs in the background
			_, body := get(t, "http://"+u.Host+"/api/version", map[string]string{"X-UI-Token": s.uiToken})
			got = nil
			json.Unmarshal([]byte(body), &got)
			if got["updateAvailable"] == true || !strings.HasPrefix(cfg.Version, "v0.2") {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		return got
	}

	got := versionInfo(Config{Version: "v0.2.2", UpdateURL: gh.URL})
	if got["version"] != "v0.2.2" || got["updateAvailable"] != true || got["latest"] != "v0.3.0" ||
		got["url"] != "https://github.com/lanrat/franklinwh/releases/tag/v0.3.0" {
		t.Errorf("older release: %v", got)
	}

	got = versionInfo(Config{Version: "v0.3.0", UpdateURL: gh.URL})
	time.Sleep(100 * time.Millisecond)
	if got["version"] != "v0.3.0" || got["updateAvailable"] != nil {
		t.Errorf("current release: %v", got)
	}

	before := hits.Load()
	got = versionInfo(Config{Version: "dev", UpdateURL: gh.URL})
	time.Sleep(100 * time.Millisecond)
	if got["version"] != "dev" || got["updateAvailable"] != nil || hits.Load() != before {
		t.Errorf("dev build: %v, GitHub hits %d -> %d", got, before, hits.Load())
	}
}
