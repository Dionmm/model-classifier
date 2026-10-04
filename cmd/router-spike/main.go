// Command router-spike logs raw subagent-spawn hook payloads from Claude Code
// and Copilot CLI, and analyses them offline. See README.md.
package main

import (
	"fmt"
	"os"
)

const usage = `usage:
  router-spike hook  --source claude-style|copilot-native [--event NAME] [--log-all] [--dir DIR]
  router-spike stats [--file FILE]
  router-spike bench [--file FILE] [--limit N] [--modes fresh,reuse] [--model M] [--out FILE]
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "hook":
		runHook(os.Args[2:]) // never returns a non-zero exit
	case "stats":
		os.Exit(runStats(os.Args[2:]))
	case "bench":
		os.Exit(runBench(os.Args[2:]))
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}
