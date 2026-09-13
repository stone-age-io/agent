//go:build windows

package nebula

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// windowsTunHint returns the note to append when Nebula fails to start on
// Windows.
//
// TUN on Windows is Wintun, and Nebula loads wintun.dll from a fixed path
// relative to the executable (overlay/tun_windows.go, checkWinTunExists). When it
// is missing, the error that reaches here is whatever LoadDLL said — "The system
// cannot find the path specified." — which names neither the DLL nor the path,
// and is the single most likely way this feature fails on a Windows host.
//
// This is a HINT APPENDED TO A FAILURE, not a preflight check, and deliberately
// so. Checking the path ourselves before calling Nebula would mean hard-coding a
// path out of Nebula's internals: if Nebula ever moved it, our check would refuse
// to start a host that actually works. A hint cannot do that. The cost is that it
// is appended to Windows start failures that have nothing to do with Wintun,
// which is why it is worded as a condition rather than a diagnosis.
func windowsTunHint() string {
	arch := runtime.GOARCH
	if arch == "386" {
		// wintun ships 386 as x86, and Nebula matches that when it builds the path.
		arch = "x86"
	}

	exe, err := os.Executable()
	if err != nil {
		return "\n\nIf this is a Wintun error: Nebula needs wintun.dll under dist\\windows\\wintun\\bin\\" + arch + " beside the agent executable. See docs/nebula.md."
	}

	want := filepath.Join(filepath.Dir(exe), "dist", "windows", "wintun", "bin", arch, "wintun.dll")
	if _, statErr := os.Stat(want); statErr == nil {
		// The DLL is where Nebula looks, so this failure is something else and a
		// Wintun hint would only mislead.
		return ""
	}

	return fmt.Sprintf("\n\nIf this is a Wintun error: no wintun.dll at %s. Nebula on Windows needs it there, and it is not shipped with the agent. See docs/nebula.md.", want)
}
