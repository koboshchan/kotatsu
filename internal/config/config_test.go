package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestURL(t *testing.T) {
	for _, s := range []string{"http://example.com", "https://u:p@example.com", "https://example.com/path", "https://example.com/?token=x", "file:///tmp/a"} {
		if ValidateURL(s, false) == nil {
			t.Fatal(s)
		}
	}
	if ValidateURL("https://example.com", false) != nil || ValidateURL("http://localhost:1234", true) != nil {
		t.Fatal("valid URL rejected")
	}
}
func TestPrivateJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "config")
	if err := PrivateJSON(p, Client{Token: "private"}); err != nil {
		t.Fatal(err)
	}
	s, _ := os.Stat(p)
	if s.Mode().Perm() != 0600 {
		t.Fatal(s.Mode())
	}
}
