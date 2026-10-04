//go:build !unix

package client

import "os"

func tryLock(*os.File) error { return nil }
func unlock(*os.File)        {}
