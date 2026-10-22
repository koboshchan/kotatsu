package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/koboshchan/kotatsu/internal/agent"
	"github.com/koboshchan/kotatsu/internal/version"
	"github.com/spf13/cobra"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	root := &cobra.Command{Use: "kotatsu-agent", Version: version.Version, SilenceUsage: true, SilenceErrors: true}
	var state, server string
	var dev bool
	root.PersistentFlags().StringVar(&state, "state", "/var/lib/kotatsu/agent.json", "private agent state file")
	enroll := &cobra.Command{Use: "enroll", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if os.Geteuid() == 0 {
			return errors.New("enroll and run the agent as an unprivileged account")
		}
		token, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return errors.New("provide enrollment token on stdin followed by newline")
		}
		st, err := agent.Enroll(cmd.Context(), server, strings.TrimSpace(token), state, dev)
		if err == nil {
			fmt.Fprintln(cmd.OutOrStdout(), "Enrolled machine", st.Machine.DisplayID)
		}
		return err
	}}
	enroll.Flags().StringVar(&server, "server", "", "server origin")
	_ = enroll.MarkFlagRequired("server")
	enroll.Flags().BoolVar(&dev, "dev", false, "allow development HTTP")
	runCmd := &cobra.Command{Use: "run", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if os.Geteuid() == 0 {
			return errors.New("agent must not run as root")
		}
		st, err := agent.Load(state)
		if err != nil {
			return err
		}
		return agent.Run(cmd.Context(), st)
	}}
	root.AddCommand(enroll, runCmd)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return root.ExecuteContext(ctx)
}
