//go:build failpoints

package failpoint

import (
	"os"
	"path/filepath"
)

// Only explicitly tagged integration builds contain abrupt process termination.
// A marker allows the harness to kill the process once and restart it safely.
func Hit(name string) {
	if os.Getenv("FAILPOINT") == name {
		dir := os.Getenv("FAILPOINT_DIR")
		if dir == "" {
			return
		}
		f, e := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e == nil {
			f.Close()
			os.Exit(86)
		}
	}
}
