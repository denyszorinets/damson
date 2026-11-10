//go:build !winrmwindows

package winrm_test

import "go.uber.org/goleak"

func leakOptions() []goleak.Option { return nil }
