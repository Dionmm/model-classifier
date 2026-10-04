package main

import "strings"

// scanDir finds a --dir/-dir value anywhere in args. The flag package stops
// at the first bad flag or stray word, so a later --dir would otherwise be
// ignored and records would land in the default directory.
func scanDir(args []string) string {
	dir := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "dir" {
			continue
		}
		if hasVal {
			dir = val
		} else if i+1 < len(args) {
			i++
			dir = args[i]
		}
	}
	return dir
}
