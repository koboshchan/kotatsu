//go:build !linux

package agent

import (
	"context"
	"errors"
	"github.com/coder/websocket"
	"github.com/koboshchan/anyshell/internal/relay"
	"os"
)

type shellSession struct {
	input  chan []byte
	ctx    context.Context
	pty    *os.File
	cancel context.CancelFunc
}

func startShell(context.Context, *websocket.Conn, relay.Frame) (*shellSession, error) {
	return nil, errors.New("agent PTYs require Linux")
}
func (s *shellSession) run()                        {}
func (s *shellSession) resize(uint16, uint16) error { return errors.New("Linux required") }
