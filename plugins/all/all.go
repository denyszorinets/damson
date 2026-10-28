// Package all lists the built-in plugins explicitly. There is no init()
// registration: add each built-in package to Plugins by hand.
package all

import (
	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/ssh"
)

// Plugins returns every built-in plugin.
func Plugins() []pluginsdk.Plugin {
	var ps []pluginsdk.Plugin
	ps = append(ps, ssh.Plugins()...)
	return ps
}
