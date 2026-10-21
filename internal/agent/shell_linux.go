//go:build linux

package agent

import (
	"context"
	"github.com/coder/websocket"
	"github.com/creack/pty"
	"github.com/koboshchan/anyshell/internal/relay"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type shellSession struct {
	ctx    context.Context
	cancel context.CancelFunc
	pty    *os.File
	cmd    *exec.Cmd
	conn   *websocket.Conn
	id     string
	input  chan []byte
}

func startShell(ctx context.Context, c *websocket.Conn, f relay.Frame) (*shellSession, error) {
	session, cancel := context.WithCancel(ctx)
	cmd := exec.Command("/bin/sh", "-l")
	// No server-supplied program, environment assignments or shell commands.
	cmd.Env = append(os.Environ(), "TERM="+f.Term)
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: f.Rows, Cols: f.Cols})
	if err != nil {
		cancel()
		return nil, err
	}
	return &shellSession{ctx: session, cancel: cancel, pty: file, cmd: cmd, conn: c, id: f.Session, input: make(chan []byte, 16)}, nil
}
func (s *shellSession) resize(rows, cols uint16) error {
	return pty.Setsize(s.pty, &pty.Winsize{Rows: rows, Cols: cols})
}
func (s *shellSession) run() {
	defer s.cancel()
	defer s.pty.Close()
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
			killShellSession(s.cmd.Process.Pid)
			_ = s.pty.Close()
		case <-finished:
		}
	}()
	if relay.Write(s.ctx, s.conn, relay.Frame{Type: "ready", Session: s.id}) != nil {
		killShellSession(s.cmd.Process.Pid)
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
	killShellSession(s.cmd.Process.Pid)
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

// Interactive job control can create multiple process groups in one login
// session. Kill same-session descendants as well as the initial shell group.
func killShellSession(id int) {
	_ = syscall.Kill(-id, syscall.SIGKILL)
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		end := strings.LastIndex(string(b), ") ")
		if end < 0 {
			continue
		}
		fields := strings.Fields(string(b[end+2:]))
		if len(fields) < 4 {
			continue
		}
		sid, err := strconv.Atoi(fields[3])
		if err == nil && sid == id {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	_ = syscall.Kill(-id, syscall.SIGKILL)
}
