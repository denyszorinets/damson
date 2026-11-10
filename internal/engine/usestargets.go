package engine

import (
	"encoding/json"
	"strings"
)

// UsesTargets reports whether a job can read the run's target list: it sets
// spec.targets, or the word "targets" appears anywhere in its spec (a
// for_each, a condition or an interpolation). It errs on the side of yes, so
// callers skip listing the inventory only for jobs that cannot need it. Pure.
func UsesTargets(job *Job) bool {
	if job == nil {
		return false
	}
	if job.Spec.Targets != nil {
		return true
	}
	b, err := json.Marshal(job.Spec)
	if err != nil {
		return true
	}
	return strings.Contains(string(b), "targets")
}
