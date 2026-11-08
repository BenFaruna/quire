//go:build !linux

package main

// Only WebKitGTK on Linux shares SIGUSR1 with the Go runtime.
func releaseSIGUSR1() {}
