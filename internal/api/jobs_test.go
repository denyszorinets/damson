package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/store"
)

func TestValidateJob(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		tests := []struct {
			name         string
			spec         string
			valid        bool
			errPath      string // prefix of the first error path
			warningPaths []string
		}{
			{name: "valid", spec: specDoc("j", "", "ssh", `{"host":"h"}`), valid: true},
			{name: "bad step id", spec: strings.Replace(specDoc("j", "", "ssh", `{"host":"h"}`), `"id":"a"`, `"id":"A b"`, 1), errPath: "spec.steps[0].id"},
			{name: "unknown plugin is a warning", spec: specDoc("j", "", "nosuch", `{"x":1}`), valid: true, warningPaths: []string{"spec.steps[0].plugin"}},
			{name: "input fails the plugin schema", spec: specDoc("j", "", "ssh", `{"host":1}`), errPath: "spec.steps[0].with.host"},
			{name: "missing required input", spec: specDoc("j", "", "ssh", `{}`), errPath: "spec.steps[0].with"},
			{name: "expression input is checked at run time", spec: specDoc("j", "", "ssh", `{"host":"${{ inputs.h }}"}`), valid: true},
			{name: "reserved field", spec: strings.Replace(specDoc("j", "", "ssh", `{"host":"h"}`), `"steps"`, `"concurrency":{"max":1},"steps"`, 1), errPath: "spec.concurrency"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var out struct {
					Valid  bool `json:"valid"`
					Errors []struct {
						Path string `json:"path"`
					} `json:"errors"`
					Warnings []struct {
						Path string `json:"path"`
					} `json:"warnings"`
				}
				f.callOK(t, http.MethodPost, "/api/v1/jobs/validate", wrapSpec(tt.spec), http.StatusOK, &out)
				assert.Equal(t, tt.valid, out.Valid)
				if tt.errPath != "" {
					require.NotEmpty(t, out.Errors)
					assert.True(t, strings.HasPrefix(out.Errors[0].Path, tt.errPath), "first error path %q", out.Errors[0].Path)
				} else {
					assert.Empty(t, out.Errors)
				}
				var got []string
				for _, w := range out.Warnings {
					got = append(got, w.Path)
				}
				assert.Equal(t, tt.warningPaths, got)
			})
		}
	})
}

func TestJobsAndVersions(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		spec1 := specDoc("deploy", "", "ssh", `{"host":"one"}`)
		d := f.createJob(t, spec1)
		assert.Equal(t, "deploy", d.Job.Name)
		assert.Equal(t, "default", d.Job.Project)
		assert.Equal(t, 1, d.Version.Version)
		assert.NotEmpty(t, d.Version.Checksum)
		assert.JSONEq(t, spec1, string(d.Version.Spec))

		// An unknown plugin is accepted with a warning.
		w := f.createJob(t, specDoc("other", "", "nosuch", `{}`))
		require.Len(t, w.Warnings, 1)

		var p problemBody
		f.callOK(t, http.MethodPost, "/api/v1/jobs", wrapSpec(spec1), http.StatusConflict, &p)
		f.callOK(t, http.MethodPost, "/api/v1/jobs", wrapSpec(specDoc("bad", "", "ssh", `{"host":1}`)), http.StatusUnprocessableEntity, &p)
		assert.Contains(t, p.locations(), "spec.steps[0].with.host")
		f.callOK(t, http.MethodPost, "/api/v1/jobs", `{"name":"x","spec":`+specDoc("y", "", "ssh", `{"host":"h"}`)+`}`, http.StatusUnprocessableEntity, &p)
		assert.Contains(t, p.locations(), "name")
		f.callOK(t, http.MethodPost, "/api/v1/jobs", wrapSpec(strings.Replace(spec1, `"project":"default"`, `"project":"nope"`, 1)), http.StatusUnprocessableEntity, &p)

		var list struct {
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
			NextCursor string `json:"next_cursor"`
		}
		f.callOK(t, http.MethodGet, "/api/v1/jobs", "", http.StatusOK, &list)
		assert.Len(t, list.Items, 2)
		f.callOK(t, http.MethodGet, "/api/v1/jobs?limit=1", "", http.StatusOK, &list)
		require.Len(t, list.Items, 1)
		assert.NotEmpty(t, list.NextCursor)
		f.callOK(t, http.MethodGet, "/api/v1/jobs?project=nope", "", http.StatusNotFound, nil)

		var got jobDetailBody
		f.callOK(t, http.MethodGet, "/api/v1/jobs/"+d.Job.ID, "", http.StatusOK, &got)
		assert.Equal(t, 1, got.Job.CurrentVersion)
		f.callOK(t, http.MethodGet, "/api/v1/jobs/nope", "", http.StatusNotFound, nil)

		// Version 2, then a stale write based on version 1.
		spec2 := specDoc("deploy", "", "ssh", `{"host":"two"}`)
		var v2 jobDetailBody
		f.callOK(t, http.MethodPost, "/api/v1/jobs/"+d.Job.ID+"/versions", `{"expected_version":1,"spec":`+spec2+`}`, http.StatusCreated, &v2)
		assert.Equal(t, 2, v2.Version.Version)
		assert.Equal(t, 2, v2.Job.CurrentVersion)
		f.callOK(t, http.MethodPost, "/api/v1/jobs/"+d.Job.ID+"/versions", `{"expected_version":1,"spec":`+spec2+`}`, http.StatusConflict, &p)
		assert.Contains(t, p.Type, "stale")
		f.callOK(t, http.MethodPost, "/api/v1/jobs/"+d.Job.ID+"/versions", wrapSpec(specDoc("renamed", "", "ssh", `{"host":"h"}`)), http.StatusUnprocessableEntity, &p)
		f.callOK(t, http.MethodPost, "/api/v1/jobs/nope/versions", wrapSpec(spec2), http.StatusNotFound, nil)
		// No expected_version means "the current one".
		f.callOK(t, http.MethodPost, "/api/v1/jobs/"+d.Job.ID+"/versions", wrapSpec(spec1), http.StatusCreated, &v2)
		assert.Equal(t, 3, v2.Version.Version)

		var vs struct {
			Items []struct {
				Version int             `json:"version"`
				Spec    json.RawMessage `json:"spec"`
			} `json:"items"`
			NextCursor string `json:"next_cursor"`
		}
		f.callOK(t, http.MethodGet, "/api/v1/jobs/"+d.Job.ID+"/versions?limit=2", "", http.StatusOK, &vs)
		require.Len(t, vs.Items, 2)
		assert.Equal(t, 1, vs.Items[0].Version)
		assert.Empty(t, vs.Items[0].Spec, "lists omit the spec")
		cursor := vs.NextCursor
		require.NotEmpty(t, cursor)
		vs.NextCursor = ""
		f.callOK(t, http.MethodGet, "/api/v1/jobs/"+d.Job.ID+"/versions?limit=2&cursor="+cursor, "", http.StatusOK, &vs)
		require.Len(t, vs.Items, 1)
		assert.Equal(t, 3, vs.Items[0].Version)
		assert.Empty(t, vs.NextCursor)

		var one struct {
			Version int             `json:"version"`
			Spec    json.RawMessage `json:"spec"`
		}
		f.callOK(t, http.MethodGet, "/api/v1/jobs/"+d.Job.ID+"/versions/2", "", http.StatusOK, &one)
		assert.Equal(t, 2, one.Version)
		assert.JSONEq(t, spec2, string(one.Spec))
		f.callOK(t, http.MethodGet, "/api/v1/jobs/"+d.Job.ID+"/versions/9", "", http.StatusNotFound, nil)
	})
}
