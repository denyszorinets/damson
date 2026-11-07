package gitcheckout

import (
	"errors"
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

func TestClassify(t *testing.T) {
	for stderr, want := range map[string]pluginsdk.ErrorKind{
		"fatal: Authentication failed for 'https://h/r.git/'":                                    pluginsdk.KindAuth,
		"git@h: Permission denied (publickey).":                                                  pluginsdk.KindAuth,
		"Host key verification failed.":                                                          pluginsdk.KindAuth,
		"fatal: could not read Username for 'https://h': terminal prompts disabled":              pluginsdk.KindAuth,
		"fatal: unable to access 'https://h/r.git/': Could not resolve host: h":                  pluginsdk.KindConnection,
		"ssh: connect to host h port 22: Connection refused":                                     pluginsdk.KindConnection,
		"fatal: couldn't find remote ref nope":                                                   pluginsdk.KindValidation,
		"fatal: repository 'https://h/r.git/' not found":                                         pluginsdk.KindValidation,
		"error: something unexpected":                                                            pluginsdk.KindExitCode,
		"fatal: remote error: upload-pack: not our ref 0123456789012345678901234567890123456789": pluginsdk.KindValidation,
	} {
		err := classify("fetch", stderr, errors.New("exit status 128"))
		se, ok := err.(*pluginsdk.StepError)
		if !ok || se.Kind != want {
			t.Errorf("%q -> %v, want %s", stderr, err, want)
		}
	}
}
