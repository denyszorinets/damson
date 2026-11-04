package secrets

import (
	"errors"
	"testing"
)

func TestAuthorize(t *testing.T) {
	cases := []struct {
		name     string
		project  string
		ref      string
		fromNode bool
		ok       bool
	}{
		{"own project", "billing", "secret://billing/db", false, true},
		{"ancestor", "org/team/app", "secret://org/team/db", false, true},
		{"ancestor top", "org/team/app", "secret://org/db", false, true},
		{"sibling", "org/team/app", "secret://org/team/other/db", false, false},
		{"other project", "billing", "secret://shipping/db", false, false},
		{"child scope", "org", "secret://org/team/db", false, false},
		{"prefix of segment name only", "org-x", "secret://org/db", false, false},
		{"literal inventory", "billing", "secret://_inventory/key", false, false},
		{"node credential", "billing", "secret://_inventory/key", true, true},
		{"node credential in project scope", "billing", "secret://billing/key", true, true},
		{"node credential other project", "billing", "secret://shipping/key", true, false},
	}
	for _, c := range cases {
		r, err := ParseRef(c.ref)
		if err != nil {
			t.Fatal(err)
		}
		err = Authorize(c.project, r, c.fromNode)
		if (err == nil) != c.ok || (err != nil && !errors.Is(err, ErrDenied)) {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}
