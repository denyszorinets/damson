// Command damson is the single Damson binary: controller, agent or dev mode.
package main

import "github.com/denyszorinets/damson/pkg/damsoncli"

func main() { damsoncli.Main(damsoncli.Options{}) }
