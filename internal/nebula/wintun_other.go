//go:build !windows

package nebula

// windowsTunHint is empty everywhere but Windows. Linux and FreeBSD create the
// TUN device through the kernel, with no library to ship alongside the binary —
// they need privileges, which fail with an error that already says so.
func windowsTunHint() string { return "" }
