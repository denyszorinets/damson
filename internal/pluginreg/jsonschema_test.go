package pluginreg_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/pluginreg"
)

func TestValidateJSONSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["n"],"properties":{
		"n":{"type":"integer","minimum":1},
		"tag":{"type":"string","pattern":"^v[0-9]+$"}},"additionalProperties":false}`)
	tests := []struct {
		name    string
		schema  json.RawMessage
		input   string
		wantErr string // substring of the first field error, "" for valid
		other   bool   // a non-validation error
	}{
		{name: "valid", schema: schema, input: `{"n":2,"tag":"v1"}`},
		{name: "missing required", schema: schema, input: `{}`, wantErr: "n"},
		{name: "wrong type", schema: schema, input: `{"n":"x"}`, wantErr: "integer"},
		{name: "remote ref is denied", schema: json.RawMessage(`{"$ref":"http://example.invalid/s.json"}`), input: `{}`, other: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := pluginreg.ValidateJSONSchema("options", tt.schema, json.RawMessage(tt.input))
			switch {
			case tt.other:
				var ve pluginreg.ValidationErrors
				require.Error(t, err)
				assert.False(t, errors.As(err, &ve))
			case tt.wantErr == "":
				require.NoError(t, err)
			default:
				var ve pluginreg.ValidationErrors
				require.ErrorAs(t, err, &ve)
				assert.Contains(t, ve.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateJSONSchema_NeverEchoesInputValues(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"tag":{"type":"string","pattern":"^v[0-9]+$"}}}`)
	err := pluginreg.ValidateJSONSchema("options", schema, json.RawMessage(`{"tag":"hunter2-not-a-version"}`))
	var ve pluginreg.ValidationErrors
	require.ErrorAs(t, err, &ve)
	assert.False(t, strings.Contains(ve.Error(), "hunter2"), ve.Error())
}
