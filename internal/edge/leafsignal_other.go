//go:build !linux && !freebsd

package edge

import (
	"fmt"
	"runtime"
)

// SignalLeafReload cannot reach a separately supervised leaf on this platform,
// and does not try.
//
// On Windows nats-server takes its reload through the service control manager
// (`nats-server --signal reload=<service>`), which would mean knowing the
// service name the operator chose and holding the right to control it. Rather
// than guess at either, the file is rewritten and this says to restart the
// leaf; the leaf_preload check keeps saying so until the leaf has loaded it. An
// embedded leaf is unaffected: it is reloaded in-process on every platform.
func SignalLeafReload(pidFile string) error {
	return fmt.Errorf("cannot signal a separately supervised leaf to reload on %s: restart the leaf to apply the new nats-leaf.conf", runtime.GOOS)
}
