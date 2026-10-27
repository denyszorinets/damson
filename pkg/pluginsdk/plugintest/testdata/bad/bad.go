package bad

import (
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

var counter int

func init() { counter++ }

func Plugins(x int) []pluginsdk.Plugin { _ = store.X; return nil }
