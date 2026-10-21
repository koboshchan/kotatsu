//go:build linux

package agent

import (
	"bufio"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Each PTY has its own subreaper supervisor. Detached/double-forked descendants
// are adopted by this process, including children that create another session.
// The private lifetime pipe closes on normal shutdown AND abrupt agent death.
func init() {
	if os.Getenv("_ANYSHELL_PTY_WORKER") != "1" {
		return
	}
	os.Unsetenv("_ANYSHELL_PTY_WORKER")
	os.Exit(runWorker())
}
func runWorker() int {
	if os.Geteuid() == 0 {
		return 126
	}
	control := os.NewFile(3, "anyshell-session-lifetime")
	if control == nil {
		return 126
	}
	defer control.Close()
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return 126
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	shell := exec.Command("/bin/sh", "-l")
	shell.Stdin = os.Stdin
	shell.Stdout = os.Stdout
	shell.Stderr = os.Stderr
	shell.Env = os.Environ()
	if err := shell.Start(); err != nil {
		return 126
	}
	done := make(chan struct{})
	go func() { _, _ = bufio.NewReader(control).ReadByte(); close(done) }()
	shellDone := make(chan error, 1)
	go func() { shellDone <- shell.Wait() }()
	code := 0
	select {
	case err := <-shellDone:
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else {
				code = 1
			}
		}
	case <-signals:
		code = 129
	case <-done:
		code = 129
	}
	// Freeze before killing to prevent ordinary fork/double-fork races. The
	// supervisor stays alive until adopted descendants are terminated/reaped.
	cleanupWorkerChildren()
	return code
}

type procEntry struct{ pid, parent int }

func workerChildren() []int {
	root := os.Getpid()
	entries, _ := os.ReadDir("/proc")
	processes := []procEntry{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == root {
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
		f := strings.Fields(string(b[end+2:]))
		if len(f) < 2 {
			continue
		}
		parent, err := strconv.Atoi(f[1])
		if err == nil {
			processes = append(processes, procEntry{pid: pid, parent: parent})
		}
	}
	descendants := map[int]bool{root: true}
	for {
		changed := false
		for _, p := range processes {
			if descendants[p.parent] && !descendants[p.pid] {
				descendants[p.pid] = true
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	result := []int{}
	for id := range descendants {
		if id != root {
			result = append(result, id)
		}
	}
	return result
}
func cleanupWorkerChildren() {
	deadline := time.Now().Add(2 * time.Second)
	for {
		children := workerChildren()
		if len(children) == 0 {
			return
		}
		for _, pid := range children {
			_ = syscall.Kill(pid, syscall.SIGSTOP)
		}
		// Discover any children created immediately before their parent stopped.
		for _, pid := range workerChildren() {
			_ = syscall.Kill(pid, syscall.SIGSTOP)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		for {
			var status syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}
