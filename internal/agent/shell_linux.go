//go:build linux

package agent

import (
	"context"
	"github.com/coder/websocket"
	"github.com/creack/pty"
	"github.com/koboshchan/anyshell/internal/relay"
	"os"
	"os/exec"
)

type shellSession struct {
	ctx      context.Context
	cancel   context.CancelFunc
	pty      *os.File
	cmd      *exec.Cmd
	conn     *websocket.Conn
	id       string
	input    chan []byte
	lifetime *os.File
}

func startShell(ctx context.Context, c *websocket.Conn, f relay.Frame) (*shellSession, error) {
	session, cancel := context.WithCancel(ctx)
	executable, err := os.Executable()
	if err != nil {
		cancel()
		return nil, err
	}
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		cancel()
		return nil, err
	}
	cmd := exec.Command(executable)
	cmd.Env = append(os.Environ(), "TERM="+f.Term, "_ANYSHELL_PTY_WORKER=1")
	cmd.ExtraFiles = []*os.File{readEnd}
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: f.Rows, Cols: f.Cols})
	_ = readEnd.Close()
	if err != nil {
		cancel()
		writeEnd.Close()
		return nil, err
	}
	return &shellSession{ctx: session, cancel: cancel, pty: file, cmd: cmd, conn: c, id: f.Session, input: make(chan []byte, 16), lifetime: writeEnd}, nil
}
func (s *shellSession) resize(rows, cols uint16) error {
	return pty.Setsize(s.pty, &pty.Winsize{Rows: rows, Cols: cols})
}
func (s *shellSession) run() {
	defer s.cancel()
	defer s.pty.Close()
	defer s.lifetime.Close()
	inputDone := make(chan struct{})
	defer func() { s.cancel(); _ = s.pty.Close(); <-inputDone }()
	go func() {
		defer close(inputDone)
		for {
			select {
			case <-s.ctx.Done():
				return
			case b := <-s.input:
				if _, err := s.pty.Write(b); err != nil {
					return
				}
			}
		}
	}()
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-s.ctx.Done():
			_ = s.lifetime.Close()
			_ = s.pty.Close()
		case <-finished:
		}
	}()
	if relay.Write(s.ctx, s.conn, relay.Frame{Type: "ready", Session: s.id}) != nil {
		_ = s.lifetime.Close()
		_ = s.cmd.Wait()
		return
	}
	buffer := make([]byte, relay.MaxData)
	for {
		n, err := s.pty.Read(buffer)
		if n > 0 {
			if relay.Write(s.ctx, s.conn, relay.Frame{Type: "data", Session: s.id, Data: buffer[:n]}) != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	_ = s.lifetime.Close()
	err := s.cmd.Wait()
	code := 0
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		} else {
			code = 1
		}
	}
	if s.ctx.Err() == nil {
		_ = relay.Write(s.ctx, s.conn, relay.Frame{Type: "exit", Session: s.id, Code: code})
	}
}
