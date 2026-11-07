// Package all lists the built-in plugins explicitly. There is no init()
// registration: add each built-in package to Plugins by hand.
package all

import (
	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/ansible"
	"github.com/denyszorinets/damson/plugins/ssh"
	"github.com/denyszorinets/damson/plugins/transfer"
	"github.com/denyszorinets/damson/plugins/winrm"
)

// Plugins returns every built-in plugin.
func Plugins() []pluginsdk.Plugin {
	var ps []pluginsdk.Plugin
	ps = append(ps, ansible.Plugins()...)
	ps = append(ps, ssh.Plugins()...)
	ps = append(ps, transfer.Plugins()...)
	ps = append(ps, winrm.Plugins()...)
	return ps
}
