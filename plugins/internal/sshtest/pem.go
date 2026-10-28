//go:build unix

package sshtest

import (
	"encoding/pem"
)

func pemEncode(b *pem.Block) []byte { return pem.EncodeToMemory(b) }
