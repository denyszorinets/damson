// Package all lists the built-in plugins explicitly. There is no init()
// registration: add each built-in package to Plugins by hand.
package all

import (
	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/ssh"
	"github.com/denyszorinets/damson/plugins/transfer"
)

// Plugins returns every built-in plugin.
func Plugins() []pluginsdk.Plugin {
	var ps []pluginsdk.Plugin
	ps = append(ps, ssh.Plugins()...)
	ps = append(ps, transfer.Plugins()...)
	return ps
}
