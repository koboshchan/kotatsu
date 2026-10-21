//go:build !windows

package client

import (
	"context"
	"golang.org/x/term"
	"os"
	"os/signal"
	"syscall"
)

func watchResize(ctx context.Context, fd int, send func(uint16, uint16)) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ch:
				if fd < 0 {
					continue
				}
				w, h, e := term.GetSize(fd)
				if e == nil && w > 0 && w <= 1000 && h > 0 && h <= 1000 {
					send(uint16(h), uint16(w))
				}
			}
		}
	}()
	return func() { signal.Stop(ch); close(done) }
}
