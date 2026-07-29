package settings

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

// EnvironmentTarget supplies environment operations for transactional updates.
type EnvironmentTarget struct {
	Lookup func(string) (string, bool)
	Set    func(string, string) error
	Unset  func(string) error
}

// ProcessEnvironment returns a target backed by the current process environment.
func ProcessEnvironment() EnvironmentTarget {
	return EnvironmentTarget{
		Lookup: os.LookupEnv,
		Set:    os.Setenv,
		Unset:  os.Unsetenv,
	}
}

type environmentValue struct {
	value   string
	present bool
}

// ApplyEnvironment validates and transactionally overlays values on target.
func ApplyEnvironment(values map[string]string, target EnvironmentTarget) error {
	if target.Lookup == nil || target.Set == nil || target.Unset == nil {
		return errors.New("environment target is incomplete")
	}

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := validateEnvironmentEntry(name, values[name]); err != nil {
			return err
		}
	}

	previous := make(map[string]environmentValue, len(names))
	for _, name := range names {
		value, present := target.Lookup(name)
		previous[name] = environmentValue{value: value, present: present}
	}

	applied := make([]string, 0, len(names))
	for _, name := range names {
		applied = append(applied, name)
		if err := target.Set(name, values[name]); err != nil {
			applyErr := fmt.Errorf("set environment variable %q: %w", name, err)
			return errors.Join(applyErr, rollbackEnvironment(applied, previous, target))
		}
	}
	return nil
}

func validateEnvironmentEntry(name, value string) error {
	switch {
	case name == "":
		return errors.New("environment variable name is empty")
	case strings.ContainsRune(name, '='):
		return fmt.Errorf("environment variable %q contains '='", name)
	case strings.ContainsRune(name, 0):
		return fmt.Errorf("environment variable %q contains NUL", name)
	case strings.ContainsRune(value, 0):
		return fmt.Errorf("environment variable %q has a value containing NUL", name)
	default:
		return nil
	}
}

func rollbackEnvironment(
	names []string,
	previous map[string]environmentValue,
	target EnvironmentTarget,
) error {
	var rollbackErrors []error
	for _, name := range slices.Backward(names) {
		prior := previous[name]
		var err error
		if prior.present {
			err = target.Set(name, prior.value)
		} else {
			err = target.Unset(name)
		}
		if err != nil {
			rollbackErrors = append(
				rollbackErrors,
				fmt.Errorf("restore environment variable %q: %w", name, err),
			)
		}
	}
	return errors.Join(rollbackErrors...)
}
