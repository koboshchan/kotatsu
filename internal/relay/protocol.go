// Package relay defines the bounded PTY protocol. There is no command string,
// forwarding destination, or network-listener message in this protocol.
package relay

import (
	"context"
	"errors"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

const MaxData = 32 << 10
const MaxMessage = 64 << 10

type Frame struct {
	Type    string `json:"type"`
	Session string `json:"session,omitempty"`
	Data    []byte `json:"data,omitempty"`
	Rows    uint16 `json:"rows,omitempty"`
	Cols    uint16 `json:"cols,omitempty"`
	Term    string `json:"term,omitempty"`
	Code    int    `json:"code,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

var termName = regexp.MustCompile(`^[A-Za-z0-9_.+-]{1,64}$`)

func (f Frame) ValidSize() bool {
	return f.Rows >= 1 && f.Rows <= 1000 && f.Cols >= 1 && f.Cols <= 1000
}
func (f Frame) ValidTerm() bool { return termName.MatchString(f.Term) }
func Read(ctx context.Context, c *websocket.Conn) (Frame, error) {
	var f Frame
	err := wsjson.Read(ctx, c, &f)
	if len(f.Data) > MaxData {
		return f, errors.New("data frame too large")
	}
	return f, err
}
func Write(ctx context.Context, c *websocket.Conn, f Frame) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return wsjson.Write(ctx, c, f)
}
func URL(base, path string) string {
	u, _ := url.Parse(base)
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = path
	return u.String()
}
func Dial(ctx context.Context, base, path, credential string) (*websocket.Conn, error) {
	c, _, err := websocket.Dial(ctx, URL(base, path), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + credential}}, HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	if err != nil {
		return nil, errors.New("WebSocket connection failed")
	}
	c.SetReadLimit(MaxMessage)
	return c, nil
}
