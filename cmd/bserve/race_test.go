//go:build race

package main_test

// A race-enabled test run also builds bserve with -race, so a data race in the server shows on its stderr.
func init() { buildFlags = append(buildFlags, "-race") }
