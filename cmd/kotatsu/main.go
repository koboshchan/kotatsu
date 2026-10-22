package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/koboshchan/kotatsu/internal/client"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := client.NewCommand().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		var exit client.ExitError
		if errors.As(err, &exit) && exit.Code > 0 && exit.Code < 256 {
			os.Exit(exit.Code)
		}
		os.Exit(1)
	}
}
