package api

import (
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
)

// Huma attaches the submitted value to its validation errors (ErrorDetail.Value
// and inside Message). The problem built from them must carry neither the
// Value nor anything but the message and location.
func TestNewHumaErrorDropsSubmittedValue(t *testing.T) {
	d := &huma.ErrorDetail{Message: "expected string", Location: "body.fields.k", Value: "THE-SECRET-VALUE"}
	p := newHumaError(422, "validation failed", d).(*Problem)
	got := p.Detail + p.Title
	for _, e := range p.Errors {
		got += e.Message + e.Location
	}
	if strings.Contains(got, "THE-SECRET-VALUE") {
		t.Fatalf("problem carries the submitted value: %+v", p)
	}
}
