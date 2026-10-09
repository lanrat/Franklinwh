package mobile

import (
	"path/filepath"
	"sync"

	"github.com/lanrat/franklinwh"
	"github.com/lanrat/franklinwh/internal/gui"
)

var (
	serverMu sync.Mutex
	server   *gui.Server
)

// StartServer starts the dashboard (the same page as the desktop GUI) on a
// random loopback port and returns its URL, which carries the access token;
// load it in a WebView. The login session is kept in dataDir/session.json.
// Device fields describe the phone and may be "". Calling it again while the
// server runs returns the same URL, so it is safe across Activity restarts.
func StartServer(dataDir, deviceModel, deviceName, osVersion string) (string, error) {
	serverMu.Lock()
	defer serverMu.Unlock()
	if server != nil {
		select {
		case <-server.Done():
			server.Close()
			server = nil
		default:
			return server.URL(), nil
		}
	}
	path := filepath.Join(dataDir, "session.json")
	cfg := gui.Config{SessionPath: path, Session: gui.LoadSession(path), Embedded: true}
	if deviceModel != "" || deviceName != "" || osVersion != "" {
		d := franklinwh.DefaultDevice
		if deviceModel != "" {
			d.Model = deviceModel
		}
		if deviceName != "" {
			d.Name = deviceName
		}
		if osVersion != "" {
			d.OSVersion = osVersion
		}
		cfg.Device = &d
	}
	s, err := gui.New(cfg)
	if err != nil {
		return "", &kindError{ErrKindOther, err}
	}
	u, err := s.Start()
	if err != nil {
		return "", &kindError{ErrKindOther, err}
	}
	server = s
	return u, nil
}

// StopServer stops the dashboard started by StartServer, if any.
func StopServer() {
	serverMu.Lock()
	defer serverMu.Unlock()
	if server != nil {
		server.Close()
		server = nil
	}
}
