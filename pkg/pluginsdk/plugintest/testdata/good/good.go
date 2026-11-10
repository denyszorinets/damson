package good

import (
	"errors"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

var errX = errors.New("x")

func Plugins() []pluginsdk.Plugin { _ = errX; return nil }
