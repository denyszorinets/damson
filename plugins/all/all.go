// Package all lists the built-in plugins explicitly. There is no init()
// registration: add each built-in package to Plugins by hand.
package all

import "github.com/denyszorinets/damson/pkg/pluginsdk"

// Plugins returns every built-in plugin. It is empty until the first built-in
// (ssh, M2) lands.
func Plugins() []pluginsdk.Plugin {
	var ps []pluginsdk.Plugin
	return ps
}
