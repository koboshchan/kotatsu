package client

import (
	"context"
	"errors"
	"fmt"
	"github.com/koboshchan/anyshell/internal/api"
	"github.com/koboshchan/anyshell/internal/relay"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"io"
	"os"
	"regexp"
)

var displayID = regexp.MustCompile(`^[1-9][0-9]{6}$`)

type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("remote shell exited with status %d", e.Code) }
func connectCommand(get func() (*Client, error)) *cobra.Command {
	return &cobra.Command{Use: "connect <machine-id>", Short: "Open an authorized interactive shell (local escape: Ctrl-])", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !displayID.MatchString(args[0]) {
			return errors.New("machine ID must contain seven digits")
		}
		c, err := get()
		if err != nil {
			return err
		}
		return c.Connect(cmd.Context(), args[0], cmd.InOrStdin(), cmd.OutOrStdout())
	}}
}

// Connect restores terminal state on every return path. Non-terminal streams
// use a default 80x24 PTY, which also permits scripted smoke tests.
func (c *Client) Connect(ctx context.Context, id string, input io.Reader, output io.Writer) error {
	socket, err := relay.Dial(ctx, c.BaseURL, api.Prefix+"/machines/"+id+"/connect", c.Token)
	if err != nil {
		return err
	}
	defer socket.CloseNow()
	session, cancel := context.WithCancel(ctx)
	defer cancel()
	rows, cols := uint16(24), uint16(80)
	var fd int = -1
	if f, ok := input.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fd = int(f.Fd())
		width, height, e := term.GetSize(fd)
		if e == nil && width > 0 && width <= 1000 && height > 0 && height <= 1000 {
			cols = uint16(width)
			rows = uint16(height)
		}
	}
	termName := os.Getenv("TERM")
	probe := relay.Frame{Term: termName}
	if !probe.ValidTerm() {
		termName = "xterm-256color"
	}
	if err = relay.Write(session, socket, relay.Frame{Type: "start", Rows: rows, Cols: cols, Term: termName}); err != nil {
		return err
	}
	ready, err := relay.Read(session, socket)
	if err != nil {
		return err
	}
	if ready.Type == "exit" {
		return ExitError{Code: ready.Code}
	}
	if ready.Type != "ready" {
		return errors.New("agent did not acknowledge session")
	}
	if fd >= 0 {
		old, e := term.MakeRaw(fd)
		if e != nil {
			return e
		}
		defer term.Restore(fd, old)
	}
	resizeDone := watchResize(session, fd, func(rows, cols uint16) {
		_ = relay.Write(session, socket, relay.Frame{Type: "resize", Rows: rows, Cols: cols})
	})
	defer resizeDone()
	go func() {
		b := make([]byte, relay.MaxData)
		for {
			n, e := input.Read(b)
			if n > 0 {
				// The escape byte detaches locally and is not forwarded to the shell.
				for i := 0; i < n; i++ {
					if b[i] == 29 {
						if i > 0 {
							_ = relay.Write(session, socket, relay.Frame{Type: "data", Data: b[:i]})
						}
						cancel()
						_ = socket.CloseNow()
						return
					}
				}
				if e = relay.Write(session, socket, relay.Frame{Type: "data", Data: b[:n]}); e != nil {
					return
				}
			}
			if e != nil {
				if errors.Is(e, io.EOF) {
					_ = relay.Write(session, socket, relay.Frame{Type: "eof"})
				} else {
					cancel()
					_ = socket.CloseNow()
				}
				return
			}
		}
	}()
	for {
		f, e := relay.Read(session, socket)
		if e != nil {
			if session.Err() != nil {
				return nil
			}
			return errors.New("session disconnected")
		}
		switch f.Type {
		case "data":
			if _, e = output.Write(f.Data); e != nil {
				return e
			}
		case "exit":
			if f.Code != 0 {
				return ExitError{Code: f.Code}
			}
			return nil
		default:
			return errors.New("unexpected session frame")
		}
	}
}
