// Package agent owns outbound enrollment and machine identity. It never opens
// a listener and never accepts account tokens.
package agent

import (
	"context"
	"errors"
	"github.com/koboshchan/anyshell/internal/api"
	"github.com/koboshchan/anyshell/internal/client"
	"github.com/koboshchan/anyshell/internal/config"
	"os"
	"runtime"
	"time"
)

type State struct {
	Server     string      `json:"server"`
	Credential string      `json:"credential"`
	Machine    api.Machine `json:"machine"`
	Dev        bool        `json:"dev"`
}

func Enroll(ctx context.Context, server, token, path string, dev bool) (State, error) {
	if runtime.GOOS != "linux" {
		return State{}, errors.New("agent requires Linux")
	}
	if _, err := os.Lstat(path); err == nil {
		return State{}, errors.New("state already exists; refusing to overwrite machine identity")
	} else if !os.IsNotExist(err) {
		return State{}, err
	}
	c, err := client.New(server, "", dev)
	if err != nil {
		return State{}, err
	}
	name, err := os.Hostname()
	if err != nil {
		return State{}, err
	}
	var out api.Enrolled
	if err = c.Do(ctx, "POST", api.Prefix+"/machines/enroll", api.EnrollRequest{Token: token, Name: name, Platform: runtime.GOOS + "/" + runtime.GOARCH}, &out); err != nil {
		return State{}, err
	}
	st := State{Server: server, Credential: out.Credential, Machine: out.Machine, Dev: dev}
	if err = config.PrivateJSON(path, st); err != nil {
		return State{}, err
	}
	return st, nil
}
func Load(path string) (State, error) { var st State; err := config.Load(path, &st); return st, err }
func Heartbeat(ctx context.Context, st State) error {
	c, err := client.New(st.Server, st.Credential, st.Dev)
	if err != nil {
		return err
	}
	return c.Do(ctx, "POST", api.Prefix+"/agent/heartbeat", nil, nil)
}
func Run(ctx context.Context, st State) error {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		_ = Heartbeat(ctx, st)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
