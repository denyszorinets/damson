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

// laneSeg is the prefix of a lane path segment: "for_each[<key>]". Step ids
// are [a-z0-9-]+ and lane keys exclude '/', so the segment is never an id, a
// marker or part of another segment.
const laneSegPrefix = "for_each["

// LanePath is the path of the lane with key under the for_each group at group.
func LanePath(group, key string) string { return group + "/" + laneSegPrefix + key + "]" }

// laneKey reads a lane segment.
func laneKey(seg string) (string, bool) {
	if strings.HasPrefix(seg, laneSegPrefix) && strings.HasSuffix(seg, "]") && len(seg) > len(laneSegPrefix)+1 {
		return seg[len(laneSegPrefix) : len(seg)-1], true
	}
	return "", false
}

// StepIDOf is the id of the spec step an instance path belongs to: its last
// segment, or for a lane row (whose last segment is the lane) the for_each
// group's id before it.
func StepIDOf(path string) string {
	segs := strings.Split(path, "/")
	last := segs[len(segs)-1]
	if _, ok := laneKey(last); ok && len(segs) > 1 {
		return segs[len(segs)-2]
	}
	return last
}

// laneOf splits a path at its first lane segment: the for_each group's path and
// the lane key. ok is false when the path has no lane segment.
func laneOf(path string) (group, key string, ok bool) {
	segs := strings.Split(path, "/")
	for i := 1; i < len(segs); i++ {
		if k, isLane := laneKey(segs[i]); isLane {
			return strings.Join(segs[:i], "/"), k, true
		}
	}
	return "", "", false
}

// StepAt returns the spec step a step instance path refers to. A lane path
// ("<group>/for_each[<key>]/...") resolves to the for_each group's own spec:
// a lane is a full instance of the group. After a for_each group the next
// segment must be a lane segment.
func (j *Job) StepAt(path string) (Step, bool) {
	list := j.Spec.Steps
	segs := strings.Split(path, "/")
	for i := 0; i < len(segs); i++ {
		var found *Step
		for k := range list {
			if list[k].ID == segs[i] {
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
		if found.ForEach != "" {
			if _, ok := laneKey(segs[i+1]); !ok {
				return Step{}, false
			}
			i++
			if i == len(segs)-1 {
				return *found, true // the lane row itself
			}
		}
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
