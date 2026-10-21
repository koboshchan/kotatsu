package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/coder/websocket"
	"github.com/koboshchan/anyshell/internal/agent"
	"github.com/koboshchan/anyshell/internal/api"
	"github.com/koboshchan/anyshell/internal/config"
	"github.com/koboshchan/anyshell/internal/relay"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestInstallerUnsupportedAndChecksumFailure(t *testing.T) {
	s := testServer(t)
	a, _ := s.Store.Register(context.Background(), "installer")
	e, _ := s.Store.CreateEnrollment(context.Background(), a.Account.ID, false, time.Minute)
	s.Config.ReleaseVersion = "v0.1.0"
	s.Config.ReleaseURL = "http://localhost"
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/script/"+e.Token, nil))
	for _, tc := range []struct {
		name, arch string
		want       string
	}{
		{"unsupported", "riscv64", "unsupported architecture"},
		{"amd64-bad-checksum", "x86_64", "checksum verification failed"},
		{"arm64-bad-checksum", "aarch64", "checksum verification failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			os.Mkdir(bin, 0700)
			write := func(name, body string) {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			write("uname", "if [ \"$1\" = -s ]; then echo Linux; else echo "+tc.arch+"; fi")
			write("id", "echo 0")
			write("systemctl", "exit 0")
			// Curl flags are intentionally parsed rather than depending on their order.
			write("curl", `url= out=
while [ "$#" -gt 0 ]; do case "$1" in -o) shift; out=$1;; http*) url=$1;; esac; shift; done
case "$url" in *SHA256SUMS) printf '0000000000000000000000000000000000000000000000000000000000000000  anyshell-agent_v0.1.0_linux_amd64.tar.gz\n0000000000000000000000000000000000000000000000000000000000000000000  anyshell-agent_v0.1.0_linux_arm64.tar.gz\n' > "$out";; *) echo corrupt > "$out";; esac`)
			command := exec.Command("/bin/sh")
			command.Stdin = strings.NewReader(w.Body.String())
			command.Env = append(os.Environ(), "PATH="+bin+":/usr/sbin:/usr/bin:/bin")
			out, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) {
				t.Fatal(err, string(out))
			}
		})
	}
}

// This positive-path harness executes the exact rendered installer, checks a
// real checksum, extracts and executes the real Linux agent and enrolls it into
// the real API. Privileged account/systemd actions are redirected to a temp root.
func TestInstallerRealAgentInIsolatedRoot(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	os.MkdirAll(root, 0700)
	s := testServer(t)
	httpServer := httptest.NewServer(s)
	defer httpServer.Close()
	s.Config.PublicURL = httpServer.URL
	s.Config.ReleaseURL = httpServer.URL
	s.Config.ReleaseVersion = "v0.1.0"
	s.Config.ReleaseDir = filepath.Join(dir, "releases")
	os.Mkdir(s.Config.ReleaseDir, 0700)
	artifact := filepath.Join(s.Config.ReleaseDir, "anyshell-agent_v0.1.0_linux_amd64.tar.gz")
	stage := filepath.Join(dir, "stage")
	os.Mkdir(stage, 0700)
	command := exec.Command("go", "build", "-o", filepath.Join(stage, "anyshell-agent"), "./cmd/agent")
	command.Dir = "../.."
	if b, e := command.CombinedOutput(); e != nil {
		t.Fatal(e, string(b))
	}
	command = exec.Command("tar", "-czf", artifact, "-C", stage, "anyshell-agent")
	if b, e := command.CombinedOutput(); e != nil {
		t.Fatal(e, string(b))
	}
	bytes, e := os.ReadFile(artifact)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(bytes)
	os.WriteFile(filepath.Join(s.Config.ReleaseDir, "SHA256SUMS"), []byte(fmt.Sprintf("%x  %s\n", sum, filepath.Base(artifact))), 0600)
	a, _ := s.Store.Register(context.Background(), "script-owner")
	token, _ := s.Store.CreateEnrollment(context.Background(), a.Account.ID, false, time.Minute)
	response, e := http.Get(httpServer.URL + "/script/" + token.Token)
	if e != nil {
		t.Fatal(e)
	}
	body, e := io.ReadAll(response.Body)
	response.Body.Close()
	if e != nil || response.StatusCode != 200 {
		t.Fatal(e, response.StatusCode)
	}
	script := string(body)
	for _, path := range []string{"/var/lib/anyshell", "/usr/local/bin/anyshell-agent", "/etc/systemd/system"} {
		script = strings.ReplaceAll(script, path, root+path)
	}
	os.MkdirAll(root+"/usr/local/bin", 0700)
	os.MkdirAll(root+"/etc/systemd/system", 0700)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nset -eu\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write("uname", `if [ "$1" = -s ]; then echo Linux; else echo x86_64; fi`)
	write("id", "echo 0")
	write("getent", "exit 0")
	write("systemctl", `printf '%s\n' "$*" >> "$ANYSHELL_TEST_ROOT/systemctl.calls"`)
	write("runuser", `shift;shift;shift;exec "$@"`)
	write("install", `# keep real install permissions, skip only account ownership
args=
while [ "$#" -gt 0 ]; do
 case "$1" in -o|-g) shift;shift;; *) args="$args '$1'";shift;; esac
done
eval "exec /usr/bin/install $args"`)
	command = exec.Command("/bin/sh")
	command.Stdin = strings.NewReader(script)
	command.Env = append(os.Environ(), "PATH="+bin+":/usr/sbin:/usr/bin:/bin", "ANYSHELL_TEST_ROOT="+root)
	out, e := command.CombinedOutput()
	if e != nil {
		t.Fatal(e, string(out))
	}
	var state agent.State
	if e = config.Load(root+"/var/lib/anyshell/agent.json", &state); e != nil {
		t.Fatal(e)
	}
	machines, e := s.Store.Machines(context.Background(), a.Account.ID)
	if e != nil || len(machines) != 1 || machines[0].ID != state.Machine.ID {
		t.Fatal(machines, e)
	}
	unit, e := os.ReadFile(root + "/etc/systemd/system/anyshell-agent.service")
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(unit), "NoNewPrivileges=true") || strings.Contains(string(unit), token.Token) {
		t.Fatal("unit hardening/secret")
	}
	calls, _ := os.ReadFile(root + "/systemctl.calls")
	if !strings.Contains(string(calls), "enable --now anyshell-agent.service") {
		t.Fatal(string(calls))
	}
	st, _ := os.Stat(root + "/var/lib/anyshell/agent.json")
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
	// Start and stop the installed executable twice using its persisted credential,
	// proving process restart does not create another inventory record.
	for n := 0; n < 2; n++ {
		run := exec.Command(root+"/usr/local/bin/anyshell-agent", "run", "--state", root+"/var/lib/anyshell/agent.json")
		run.Env = os.Environ()
		if e = run.Start(); e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		var socket *websocket.Conn
		for ctx.Err() == nil {
			socket, e = relay.Dial(ctx, httpServer.URL, api.Prefix+"/machines/"+state.Machine.DisplayID+"/connect", a.Token.Token)
			if e == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		if socket == nil {
			run.Process.Kill()
			run.Wait()
			t.Fatal("installed agent did not reconnect", e)
		}
		socket.CloseNow()
		run.Process.Signal(syscall.SIGTERM)
		if e = run.Wait(); e != nil {
			t.Fatal(e)
		}
	}
	machines, _ = s.Store.Machines(context.Background(), a.Account.ID)
	if len(machines) != 1 {
		t.Fatal(machines)
	}
}
