package config

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ValidateURL intentionally requires a separate development opt-in for cleartext.
func ValidateURL(raw string, dev bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("server must be an absolute origin without credentials, path, query or fragment")
	}
	if u.Scheme != "https" && !(dev && u.Scheme == "http") {
		return errors.New("HTTPS required (HTTP is development-only)")
	}
	return nil
}

func PrivateJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".kotatsu-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

type Client struct {
	Server string `json:"server"`
	Token  string `json:"token"`
	Dev    bool   `json:"dev"`
}

func ClientPath() (string, error) {
	dir, err := os.UserConfigDir()
	return filepath.Join(dir, "kotatsu", "config.json"), err
}
func Load(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
func Env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
