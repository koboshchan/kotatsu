//go:build windows

package client

import (
	"context"
	"golang.org/x/term"
	"time"
)

func watchResize(ctx context.Context, fd int, send func(uint16, uint16)) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		oldW, oldH := 0, 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				if fd < 0 {
					continue
				}
				w, h, e := term.GetSize(fd)
				if e == nil && w > 0 && w <= 1000 && h > 0 && h <= 1000 && (w != oldW || h != oldH) {
					oldW = w
					oldH = h
					send(uint16(h), uint16(w))
				}
			}
		}
	}()
	return func() { close(done) }
}
