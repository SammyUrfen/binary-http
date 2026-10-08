// Command bserve serves files over BH/1. This is a stub so the package builds.
// The tests in this folder fail against it on purpose.
package main

import "os"

// stubExit is the exit code the tests see until the real server lands.
const stubExit = 99

func main() { os.Exit(stubExit) }
