package api

import "time"

const Prefix = "/api/v1"

// IsShortCode reports whether s is a 6-character lowercase alphanumeric
// enrollment code. Long enrollment tokens are prefixed with "ae_".
func IsShortCode(s string) bool {
	if len(s) != 6 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Token struct {
	ID        string    `json:"id"`
	Token     string    `json:"token,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
type Registration struct {
	Account Account `json:"account"`
	Token   Token   `json:"token"`
}
type Enrollment struct {
	ID        string    `json:"id"`
	Token     string    `json:"token,omitempty"`
	ShortCode string    `json:"short_code,omitempty"`
	Reusable  bool      `json:"reusable"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      int       `json:"used"`
	Revoked   bool      `json:"revoked"`
}
type Machine struct {
	Revoked   bool      `json:"revoked"`
	ID        string    `json:"id"`
	DisplayID string    `json:"display_id"`
	Name      string    `json:"name"`
	Platform  string    `json:"platform"`
	PublicIP  string    `json:"public_ip"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
	Online    bool      `json:"online"`
}
type EnrollRequest struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
}
type Enrolled struct {
	Machine    Machine `json:"machine"`
	Credential string  `json:"credential"`
}
type Error struct {
	Error string `json:"error"`
}
