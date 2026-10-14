//go:build linux || freebsd

package edge

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// SignalLeafReload sends SIGHUP to the separately supervised leaf whose pid is
// in pidFile: nats-server's own "reload your config" signal, the same thing
// `nats-server --signal reload` sends.
//
// The pid file is the `pid_file` line the generated conf carries, so a leaf
// started from a conf written before that line existed has none. That is the
// common first-run case after an upgrade, and the answer is to restart the leaf
// once -- after which every later change reaches it with no restart at all.
//
// It refuses its own pid. An embedded leaf writes the agent's pid to the same
// file, and a box switched from embedded to separate can be left holding it;
// SIGHUP to a process that does not handle it terminates the process.
func SignalLeafReload(pidFile string) error {
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no %s: the leaf is not running, or was started before its config named pid_file; restart it once", pidFile)
		}
		return fmt.Errorf("read %s: %w", pidFile, err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		return fmt.Errorf("%s does not hold a usable process id (%q)", pidFile, strings.TrimSpace(string(raw)))
	}
	if pid == os.Getpid() {
		return fmt.Errorf("%s names this agent's own process, left by an embedded leaf; restart the separately supervised leaf once so it writes its own", pidFile)
	}

	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		return fmt.Errorf("send SIGHUP to the leaf (pid %d from %s): %w", pid, pidFile, err)
	}
	return nil
}
