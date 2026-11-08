//go:build linux

package main

/*
#include <signal.h>
*/
import "C"

// releaseSIGUSR1 hands SIGUSR1 back to its default disposition before the
// WebKit webview starts. Go's runtime installs a handler for every catchable
// signal at startup, and WebKit's JavaScriptCore uses SIGUSR1 to suspend
// threads while collecting garbage. When JSC finds Go's handler it logs
// "Overriding existing handler for signal 10" before taking the signal over,
// so the warning is cosmetic; handing the signal back first only keeps the
// startup log clean. Nothing in this app notifies on SIGUSR1, and JSC re-installs its
// handler as soon as the webview is created. The MCP mode never gets here.
func releaseSIGUSR1() {
	C.signal(C.SIGUSR1, C.SIG_DFL)
}
