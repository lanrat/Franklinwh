package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Session is the login state persisted between runs.
type Session struct {
	Email    string `json:"email"`
	Token    string `json:"token"`
	ClientID string `json:"clientId"`
}

// LoadSession reads the session file. A missing or unreadable file yields an
// empty session.
func LoadSession(path string) Session {
	var s Session
	if path == "" {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(os.Stderr, "warning: reading session:", err)
		}
		return s
	}
	if err := json.Unmarshal(data, &s); err != nil {
		fmt.Fprintln(os.Stderr, "warning: ignoring corrupt session file:", err)
		return Session{}
	}
	return s
}

// SaveSession writes the session file, readable only by the user since it
// holds a login token.
func SaveSession(path string, s Session) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
