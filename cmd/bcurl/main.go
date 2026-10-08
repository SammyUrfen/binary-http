package main

import "os"

// Stub so the package builds. The client worker replaces it. Exit 99 is not a README exit code,
// so every black-box test fails until the real client exists.
func main() { os.Exit(99) }
