// Package config persists Cambly CLI credentials (the session cookie + csrf
// token) to a user config file with 0600 permissions.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Credentials is what the CLI stores between runs.
type Credentials struct {
	Session string    `json:"session"`
	CSRF    string    `json:"csrf,omitempty"`
	BaseURL string    `json:"baseUrl,omitempty"`
	SavedAt time.Time `json:"savedAt"`
}

// Path returns the credentials file path. Override the directory with
// CAMBLY_CONFIG_DIR; otherwise it follows XDG_CONFIG_HOME, then ~/.config.
func Path() (string, error) {
	if dir := os.Getenv("CAMBLY_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "credentials.json"), nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "cambly", "credentials.json"), nil
}

// Load reads stored credentials. It returns (nil, nil) when none are saved.
func Load() (*Credentials, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return &c, nil
}

// Save writes credentials atomically with 0600 permissions.
func Save(c *Credentials) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if c.SavedAt.IsZero() {
		c.SavedAt = time.Now()
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Clear removes stored credentials (no error if absent).
func Clear() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
