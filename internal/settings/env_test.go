package settings

import (
	"errors"
	"maps"
	"reflect"
	"strings"
	"testing"
)

type fakeEnvironment struct {
	values     map[string]string
	operations []string
	set        func(string, string) error
	unset      func(string) error
}

func newFakeEnvironment(values map[string]string) *fakeEnvironment {
	cloned := make(map[string]string, len(values))
	maps.Copy(cloned, values)
	return &fakeEnvironment{values: cloned}
}

func (environment *fakeEnvironment) target() EnvironmentTarget {
	return EnvironmentTarget{
		Lookup: func(name string) (string, bool) {
			value, ok := environment.values[name]
			return value, ok
		},
		Set: func(name, value string) error {
			environment.operations = append(environment.operations, "set "+name+"="+value)
			environment.values[name] = value
			if environment.set != nil {
				return environment.set(name, value)
			}
			return nil
		},
		Unset: func(name string) error {
			environment.operations = append(environment.operations, "unset "+name)
			delete(environment.values, name)
			if environment.unset != nil {
				return environment.unset(name)
			}
			return nil
		},
	}
}

func TestApplyEnvironmentOverlaysInSortedOrder(t *testing.T) {
	environment := newFakeEnvironment(map[string]string{
		"A":         "old",
		"EMPTY_OLD": "",
		"UNCHANGED": "kept",
	})
	values := map[string]string{
		"Z":         "last",
		"A":         "new",
		"EMPTY_NEW": "",
	}

	if err := ApplyEnvironment(values, environment.target()); err != nil {
		t.Fatalf("ApplyEnvironment() error = %v", err)
	}
	wantValues := map[string]string{
		"A":         "new",
		"EMPTY_OLD": "",
		"EMPTY_NEW": "",
		"UNCHANGED": "kept",
		"Z":         "last",
	}
	if !reflect.DeepEqual(environment.values, wantValues) {
		t.Fatalf("environment = %#v, want %#v", environment.values, wantValues)
	}
	wantOperations := []string{"set A=new", "set EMPTY_NEW=", "set Z=last"}
	if !reflect.DeepEqual(environment.operations, wantOperations) {
		t.Fatalf("operations = %#v, want %#v", environment.operations, wantOperations)
	}
}

func TestApplyEnvironmentValidatesBeforeMutation(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]string
		wantErr string
	}{
		{name: "empty name", values: map[string]string{"": "secret-value"}, wantErr: "name is empty"},
		{name: "equals in name", values: map[string]string{"BAD=NAME": "secret-value"}, wantErr: "contains '='"},
		{name: "NUL in name", values: map[string]string{"BAD\x00NAME": "secret-value"}, wantErr: "contains NUL"},
		{name: "NUL in value", values: map[string]string{"SECRET": "secret-value\x00suffix"}, wantErr: "value containing NUL"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := newFakeEnvironment(nil)
			err := ApplyEnvironment(test.values, environment.target())
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ApplyEnvironment() error = %v, want containing %q", err, test.wantErr)
			}
			if len(environment.operations) != 0 {
				t.Fatalf("invalid environment performed operations: %#v", environment.operations)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("ApplyEnvironment() error leaked value: %v", err)
			}
		})
	}
}

func TestApplyEnvironmentRollsBackFailedTransaction(t *testing.T) {
	setErr := errors.New("setter failed")
	environment := newFakeEnvironment(map[string]string{"A": "old", "EMPTY": ""})
	environment.set = func(name, value string) error {
		if name == "C" && value == "new-c" {
			return setErr
		}
		return nil
	}

	err := ApplyEnvironment(map[string]string{
		"A": "new-a",
		"B": "new-b",
		"C": "new-c",
	}, environment.target())
	if !errors.Is(err, setErr) {
		t.Fatalf("ApplyEnvironment() error = %v, want %v", err, setErr)
	}
	wantValues := map[string]string{"A": "old", "EMPTY": ""}
	if !reflect.DeepEqual(environment.values, wantValues) {
		t.Fatalf("rolled back environment = %#v, want %#v", environment.values, wantValues)
	}
	wantOperations := []string{
		"set A=new-a",
		"set B=new-b",
		"set C=new-c",
		"unset C",
		"unset B",
		"set A=old",
	}
	if !reflect.DeepEqual(environment.operations, wantOperations) {
		t.Fatalf("rollback operations = %#v, want %#v", environment.operations, wantOperations)
	}
}

func TestApplyEnvironmentJoinsRollbackFailures(t *testing.T) {
	setErr := errors.New("setter failed")
	rollbackErr := errors.New("rollback failed")
	environment := newFakeEnvironment(nil)
	environment.set = func(name, value string) error {
		if name == "B" {
			return setErr
		}
		return nil
	}
	environment.unset = func(name string) error {
		if name == "A" {
			return rollbackErr
		}
		return nil
	}

	err := ApplyEnvironment(map[string]string{"A": "one", "B": "two"}, environment.target())
	if !errors.Is(err, setErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("ApplyEnvironment() error = %v, want joined set and rollback errors", err)
	}
}

func TestApplyEnvironmentRequiresCompleteTarget(t *testing.T) {
	if err := ApplyEnvironment(nil, EnvironmentTarget{}); err == nil {
		t.Fatal("ApplyEnvironment() error = nil, want incomplete-target error")
	}
}
