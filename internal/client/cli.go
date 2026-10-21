package client

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/koboshchan/anyshell/internal/api"
	"github.com/koboshchan/anyshell/internal/config"
	"github.com/koboshchan/anyshell/internal/version"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

func NewCommand() *cobra.Command {
	var path, server string
	var dev bool
	root := &cobra.Command{Use: "anyshell", Short: "Manage your machines and open remote PTYs", Version: version.Version, SilenceUsage: true, SilenceErrors: true}
	defaultPath, _ := config.ClientPath()
	root.PersistentFlags().StringVar(&path, "config", defaultPath, "private config file")
	root.PersistentFlags().StringVar(&server, "server", "", "server origin")
	root.PersistentFlags().BoolVar(&dev, "dev", false, "allow development HTTP")
	settings := func() (config.Client, error) {
		var cfg config.Client
		err := config.Load(path, &cfg)
		if err != nil && !os.IsNotExist(err) {
			return cfg, err
		}
		if server != "" {
			cfg.Server = server
			cfg.Dev = dev
		} else if dev {
			cfg.Dev = true
		}
		if cfg.Server == "" {
			return cfg, errors.New("set --server to your AnyShell origin")
		}
		return cfg, config.ValidateURL(cfg.Server, cfg.Dev)
	}
	get := func() (*Client, error) {
		cfg, err := settings()
		if err != nil {
			return nil, err
		}
		if cfg.Token == "" {
			return nil, errors.New("run anyshell auth login first")
		}
		return New(cfg.Server, cfg.Token, cfg.Dev)
	}
	authCmd := &cobra.Command{Use: "auth", Short: "Register, log in and manage account tokens"}
	var name string
	register := &cobra.Command{Use: "register", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := settings()
		if err != nil {
			return err
		}
		c, err := New(cfg.Server, "", cfg.Dev)
		if err != nil {
			return err
		}
		var out api.Registration
		if err = c.Do(cmd.Context(), "POST", api.Prefix+"/accounts", map[string]string{"name": name}, &out); err != nil {
			return err
		}
		cfg.Token = out.Token.Token
		// Print the issued credential even if the local filesystem write fails,
		// so an already-created account does not become inaccessible.
		fmt.Fprintln(cmd.OutOrStdout(), "Account", out.Account.ID)
		fmt.Fprintln(cmd.OutOrStdout(), "API token (shown once):", out.Token.Token)
		return config.PrivateJSON(path, cfg)
	}}
	register.Flags().StringVar(&name, "name", "", "unique account name")
	_ = register.MarkFlagRequired("name")
	login := &cobra.Command{Use: "login", Short: "Read an API token securely from the terminal or stdin", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := settings()
		if err != nil {
			return err
		}
		raw, err := readToken(cmd.InOrStdin(), cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		c, err := New(cfg.Server, raw, cfg.Dev)
		if err != nil {
			return err
		}
		var account api.Account
		if err = c.Do(cmd.Context(), "GET", api.Prefix+"/me", nil, &account); err != nil {
			return err
		}
		cfg.Token = raw
		if err = config.PrivateJSON(path, cfg); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Logged in as", account.Name)
		return nil
	}}
	logout := &cobra.Command{Use: "logout", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		var cfg config.Client
		if err := config.Load(path, &cfg); err != nil {
			return err
		}
		cfg.Token = ""
		return config.PrivateJSON(path, cfg)
	}}
	who := &cobra.Command{Use: "whoami", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		c, e := get()
		if e != nil {
			return e
		}
		var a api.Account
		if e = c.Do(cmd.Context(), "GET", api.Prefix+"/me", nil, &a); e != nil {
			return e
		}
		return printJSON(cmd.OutOrStdout(), a)
	}}
	tokens := &cobra.Command{Use: "tokens", Short: "List active token IDs (never their secrets)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		c, e := get()
		if e != nil {
			return e
		}
		var v []api.Token
		if e = c.Do(cmd.Context(), "GET", api.Prefix+"/tokens", nil, &v); e != nil {
			return e
		}
		return printJSON(cmd.OutOrStdout(), v)
	}}
	issue := &cobra.Command{Use: "token-create", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		c, e := get()
		if e != nil {
			return e
		}
		var v api.Token
		if e = c.Do(cmd.Context(), "POST", api.Prefix+"/tokens", nil, &v); e != nil {
			return e
		}
		return printJSON(cmd.OutOrStdout(), v)
	}}
	rotate := &cobra.Command{Use: "rotate", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, e := settings()
		if e != nil {
			return e
		}
		c, e := get()
		if e != nil {
			return e
		}
		var v api.Token
		if e = c.Do(cmd.Context(), "POST", api.Prefix+"/tokens/rotate", nil, &v); e != nil {
			return e
		}
		fmt.Fprintln(cmd.OutOrStdout(), "New API token (shown once):", v.Token)
		cfg.Token = v.Token
		return config.PrivateJSON(path, cfg)
	}}
	revoke := &cobra.Command{Use: "revoke <token-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !safeID(args[0]) {
			return errors.New("invalid token ID")
		}
		c, e := get()
		if e != nil {
			return e
		}
		return c.Do(cmd.Context(), "DELETE", api.Prefix+"/tokens/"+args[0], nil, nil)
	}}
	authCmd.AddCommand(register, login, logout, who, tokens, issue, rotate, revoke)
	root.AddCommand(authCmd)
	var asJSON bool
	ls := &cobra.Command{Use: "ls", Short: "List only your machines", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		c, e := get()
		if e != nil {
			return e
		}
		var machines []api.Machine
		if e = c.Do(cmd.Context(), "GET", api.Prefix+"/machines", nil, &machines); e != nil {
			return e
		}
		if asJSON {
			return printJSON(cmd.OutOrStdout(), machines)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATE\tPUBLIC IP\tADDED\tLAST SEEN")
		for _, m := range machines {
			status := "offline"
			if m.Online {
				status = "online"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", m.DisplayID, printable(m.Name), status, m.PublicIP, m.CreatedAt.Format(time.RFC3339), m.LastSeen.Format(time.RFC3339))
		}
		return w.Flush()
	}}
	ls.Flags().BoolVar(&asJSON, "json", false, "print inventory as JSON")
	root.AddCommand(ls, connectCommand(get), &cobra.Command{Use: "revoke-machine <machine-id>", Short: "Permanently revoke a machine credential and terminate its sessions", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !displayID.MatchString(args[0]) {
			return errors.New("machine ID must contain seven digits")
		}
		c, e := get()
		if e != nil {
			return e
		}
		return c.Do(cmd.Context(), "DELETE", api.Prefix+"/machines/"+args[0], nil, nil)
	}})
	enrollment := &cobra.Command{Use: "enrollment", Short: "Manage install tokens"}
	var reusable bool
	var ttl time.Duration
	create := &cobra.Command{Use: "create", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if ttl < time.Second || ttl > 24*time.Hour {
			return errors.New("TTL must be between 1s and 24h")
		}
		c, e := get()
		if e != nil {
			return e
		}
		var v api.Enrollment
		if e = c.Do(cmd.Context(), "POST", api.Prefix+"/enrollment-tokens", map[string]any{"reusable": reusable, "ttl_seconds": int(ttl.Seconds())}, &v); e != nil {
			return e
		}
		if e = printJSON(cmd.OutOrStdout(), v); e != nil {
			return e
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Installer URL (bearer secret):", c.BaseURL+"/script/"+v.Token)
		return nil
	}}
	create.Flags().BoolVar(&reusable, "reusable", false, "explicitly allow multiple enrollments until expiration")
	create.Flags().DurationVar(&ttl, "ttl", 15*time.Minute, "token lifetime (maximum 24h)")
	list := &cobra.Command{Use: "ls", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		c, e := get()
		if e != nil {
			return e
		}
		var v []api.Enrollment
		if e = c.Do(cmd.Context(), "GET", api.Prefix+"/enrollment-tokens", nil, &v); e != nil {
			return e
		}
		return printJSON(cmd.OutOrStdout(), v)
	}}
	erevoke := &cobra.Command{Use: "revoke <id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !safeID(args[0]) {
			return errors.New("invalid enrollment ID")
		}
		c, e := get()
		if e != nil {
			return e
		}
		return c.Do(cmd.Context(), "DELETE", api.Prefix+"/enrollment-tokens/"+args[0], nil, nil)
	}}
	enrollment.AddCommand(create, list, erevoke)
	root.AddCommand(enrollment)
	return root
}
func printJSON(w io.Writer, v any) error {
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
func readToken(r io.Reader, w io.Writer) (string, error) {
	if f, ok := r.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(w, "API token: ")
		b, e := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(w)
		return strings.TrimSpace(string(b)), e
	}
	b, e := bufio.NewReader(io.LimitReader(r, 4096)).ReadString('\n')
	if e != nil && e != io.EOF {
		return "", e
	}
	if strings.TrimSpace(b) == "" {
		return "", errors.New("no API token provided")
	}
	return strings.TrimSpace(b), nil
}
func safeID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for _, c := range s {
		if !(c == '-' || c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
}
