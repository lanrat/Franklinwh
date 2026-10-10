// Package version reports this build's version and checks GitHub for a newer
// release.
package version

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// version is set at build time by CI:
//
//	go build -ldflags "-X github.com/lanrat/franklinwh/internal/version.version=v1.2.3"
var version = ""

// Get returns the version of this build: the one stamped at build time, else
// the module version recorded by "go install ...@v1.2.3", else "dev".
func Get() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

// LatestReleaseURL is the GitHub API endpoint for the newest release.
const LatestReleaseURL = "https://api.github.com/repos/lanrat/franklinwh/releases/latest"

// releasePagePrefix is the only kind of link Latest returns, so a page can
// link to it safely.
const releasePagePrefix = "https://github.com/lanrat/franklinwh/releases/"

// Release is the newest published release.
type Release struct {
	Version string // tag, e.g. "v1.2.3"
	URL     string // its page on github.com
}

// Latest fetches the newest release from apiURL (normally LatestReleaseURL).
func Latest(ctx context.Context, apiURL string) (Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "franklinwh/"+Get()+" (+https://github.com/lanrat/franklinwh)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("checking for updates: %s", resp.Status)
	}
	var r struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return Release{}, fmt.Errorf("checking for updates: %w", err)
	}
	if _, ok := parse(r.TagName); !ok {
		return Release{}, fmt.Errorf("checking for updates: unexpected tag %q", r.TagName)
	}
	url := r.HTMLURL
	if !strings.HasPrefix(url, releasePagePrefix) {
		url = releasePagePrefix + "tag/" + r.TagName
	}
	return Release{Version: r.TagName, URL: url}, nil
}

// Newer reports whether latest is a later release than current. Both must
// look like v1.2.3; anything else (such as "dev") is never older.
func Newer(current, latest string) bool {
	c, ok1 := parse(current)
	l, ok2 := parse(latest)
	if !ok1 || !ok2 {
		return false
	}
	for i := range c {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// IsRelease reports whether v is a release version like v1.2.3.
func IsRelease(v string) bool {
	_, ok := parse(v)
	return ok
}

func parse(v string) ([3]int, bool) {
	var n [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if !strings.HasPrefix(v, "v") || len(parts) != 3 {
		return n, false
	}
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return n, false
		}
		n[i] = x
	}
	return n, true
}
