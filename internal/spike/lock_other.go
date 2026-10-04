//go:build !unix

package spike

import "os"

// Windows is out of scope for the spike; appends are unlocked there.
func lockFile(*os.File) error   { return nil }
func unlockFile(*os.File) error { return nil }
