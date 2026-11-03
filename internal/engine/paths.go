package engine

import "strings"

// Path segments. A step instance's path is the ids from the top of the job
// down to the step, joined by "/": a top-level step's path is its id (so rows
// written before groups existed keep theirs), a group's body child is
// "<group>/<id>", and the children of its catch and finally lists carry the
// marker segment: "<group>/catch/<id>", "<group>/finally/<id>". The markers
// cannot be step ids (validation reserves them), so a path is unambiguous.
const (
	SegCatch   = "catch"
	SegFinally = "finally"
)

// ChildPath is the path of step id inside the scope whose paths start with
// prefix ("" at the top, "<group>/" in a group's body).
func ChildPath(prefix, id string) string { return prefix + id }

// finallyOwners returns the paths of the groups whose finally list path is in,
// outermost first. A marker always follows a group id, so a segment at index 0
// is never one: a top-level step may be named finally (jobs saved before groups
// existed may), and so may a top-level group.
func finallyOwners(path string) []string {
	segs := strings.Split(path, "/")
	var owners []string
	for i := 1; i < len(segs); i++ {
		if segs[i] == SegFinally {
			owners = append(owners, strings.Join(segs[:i], "/"))
		}
	}
	return owners
}

// InFinally reports whether path is inside some group's finally list.
func InFinally(path string) bool { return len(finallyOwners(path)) > 0 }

// StepAt returns the spec step a step instance path refers to.
func (j *Job) StepAt(path string) (Step, bool) {
	list := j.Spec.Steps
	segs := strings.Split(path, "/")
	for i := 0; i < len(segs); i++ {
		seg := segs[i]
		var found *Step
		for k := range list {
			if list[k].ID == seg {
				found = &list[k]
				break
			}
		}
		if found == nil {
			return Step{}, false
		}
		if i == len(segs)-1 {
			return *found, true
		}
		if !found.IsGroup() {
			return Step{}, false
		}
		list = found.Steps
		switch segs[i+1] {
		case SegCatch:
			list = found.Catch
			i++
		case SegFinally:
			list = found.Finally
			i++
		}
		if i+1 >= len(segs) { // a marker with no id after it
			return Step{}, false
		}
	}
	return Step{}, false
}
