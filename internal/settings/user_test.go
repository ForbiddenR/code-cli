package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadUser(t *testing.T) {
	tests := []struct {
		name    string
		content *string
		want    map[string]string
		wantErr string
	}{
		{name: "missing"},
		{name: "empty", content: new(" \n\t ")},
		{name: "empty object", content: new(`{}`)},
		{
			name:    "unknown fields",
			content: new(`{"theme":"dark","env":{"VALUE":"kept"}}`),
			want:    map[string]string{"VALUE": "kept"},
		},
		{
			name: "coerces scalar values",
			content: new(`{
				"env": {
					"STRING": " value ",
					"EMPTY": "",
					"TRUE": true,
					"FALSE": false,
					"INTEGER": 42,
					"DECIMAL": 1.5,
					"EXPONENT": 1e3,
					"SMALL_DECIMAL": 1e-6,
					"SMALL_EXPONENT": 1e-7,
					"LARGE_DECIMAL": 1e20,
					"LARGE_EXPONENT": 1e21,
					"NEGATIVE_ZERO": -0,
					"NULL": null
				}
			}`),
			want: map[string]string{
				"STRING":         " value ",
				"EMPTY":          "",
				"TRUE":           "true",
				"FALSE":          "false",
				"INTEGER":        "42",
				"DECIMAL":        "1.5",
				"EXPONENT":       "1000",
				"SMALL_DECIMAL":  "0.000001",
				"SMALL_EXPONENT": "1e-7",
				"LARGE_DECIMAL":  "100000000000000000000",
				"LARGE_EXPONENT": "1e+21",
				"NEGATIVE_ZERO":  "0",
				"NULL":           "null",
			},
		},
		{
			name:    "does not expand variables",
			content: new(`{"env":{"PATH":"${HOME}/bin:$PATH","HOME_COPY":"~"}}`),
			want: map[string]string{
				"PATH":      "${HOME}/bin:$PATH",
				"HOME_COPY": "~",
			},
		},
		{name: "invalid JSON", content: new(`{"env":`), wantErr: "decode JSON"},
		{name: "trailing JSON", content: new(`{} {}`), wantErr: "multiple JSON values"},
		{name: "null root", content: new(`null`), wantErr: "root must be an object"},
		{name: "array root", content: new(`[]`), wantErr: "cannot unmarshal array"},
		{name: "null env", content: new(`{"env":null}`), wantErr: "env must be an object"},
		{name: "string env", content: new(`{"env":"value"}`), wantErr: "env must be an object"},
		{name: "array value", content: new(`{"env":{"BAD":["secret-value"]}}`), wantErr: "env.BAD"},
		{name: "object value", content: new(`{"env":{"BAD":{"token":"secret-value"}}}`), wantErr: "env.BAD"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if test.content != nil {
				if err := os.WriteFile(path, []byte(*test.content), 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			}

			got, err := LoadUser(path)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("LoadUser() error = %v, want containing %q", err, test.wantErr)
				}
				if strings.Contains(err.Error(), "secret-value") {
					t.Fatalf("LoadUser() error leaked value: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadUser() error = %v", err)
			}
			if !reflect.DeepEqual(got.Env, test.want) && !(len(got.Env) == 0 && len(test.want) == 0) {
				t.Fatalf("LoadUser().Env = %#v, want %#v", got.Env, test.want)
			}
		})
	}
}

func TestLoadUserReportsReadFailure(t *testing.T) {
	_, err := LoadUser(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "read") {
		t.Fatalf("LoadUser() error = %v, want read failure", err)
	}
}

//go:fix inline
func stringPointer(value string) *string {
	return new(value)
}
