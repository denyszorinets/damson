// Command fakeidp runs the oidctest provider on a fixed address for the browser
// tests (web/playwright.oidc.config.ts). It is a test tool: it approves every
// login at once. Users: alice (damson-admins), bob (team-ops), carol
// (team-viewers), dave (team-leads), erin (nobody), frank (extra-runners).
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/denyszorinets/damson/internal/auth/oidctest"
)

func main() {
	addr := "127.0.0.1:4191"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	iss := oidctest.NewAt(addr)
	iss.AddUser(oidctest.User{Sub: "dave-sub", Email: "dave@example.org", Name: "Dave", Groups: []string{"team-leads"}})
	iss.AddUser(oidctest.User{Sub: "erin-sub", Email: "erin@example.org", Name: "Erin", Groups: []string{"nobody"}})
	iss.AddUser(oidctest.User{Sub: "frank-sub", Email: "frank@example.org", Name: "Frank", Groups: []string{"extra-runners"}})
	fmt.Println("fakeidp listening on", iss.URL())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	iss.Close()
}
