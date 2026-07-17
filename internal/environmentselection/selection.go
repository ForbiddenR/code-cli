// Package environmentselection migrates the pure environment selection logic from
// utils/teleport/environmentSelection.ts. It computes the environment that a remote
// session would use from a list of available environments and the merged settings
// defaultEnvironmentId, without touching settings I/O itself.
package environmentselection

import (
	"slices"

	"code-cli/internal/environments"
)

// SettingSource identifies where a setting value was configured.
type SettingSource string

const (
	// SourceUser is the global user settings source (lowest priority).
	SourceUser SettingSource = "userSettings"
	// SourceProject is the shared per-directory settings source.
	SourceProject SettingSource = "projectSettings"
	// SourceLocal is the gitignored local settings source.
	SourceLocal SettingSource = "localSettings"
	// SourceFlag is the --settings flag source.
	SourceFlag SettingSource = "flagSettings"
	// SourcePolicy is the managed or remote policy settings source (highest priority).
	SourcePolicy SettingSource = "policySettings"
)

// SettingSources lists all setting sources ordered from lowest to highest priority,
// matching SETTING_SOURCES from utils/settings/constants.ts.
var SettingSources = []SettingSource{SourceUser, SourceProject, SourceLocal, SourceFlag, SourcePolicy}

// SourceSettings is the subset of per-source settings read by environment selection.
type SourceSettings struct {
	// DefaultEnvironmentID mirrors settings.remote.defaultEnvironmentId.
	DefaultEnvironmentID string
}

// SettingsProvider returns per-source settings. Missing sources return an empty value.
// The zero SourceSettings value means "no defaultEnvironmentId configured".
type SettingsProvider func(source SettingSource) SourceSettings

// Info mirrors EnvironmentSelectionInfo from environmentSelection.ts.
type Info struct {
	AvailableEnvironments     []environments.EnvironmentResource
	SelectedEnvironment       *environments.EnvironmentResource
	SelectedEnvironmentSource *SettingSource
}

// SelectEnvironment computes the environment that would be used given available
// environments and the merged defaultEnvironmentId. It mirrors the TypeScript
// getEnvironmentSelectionInfo selection logic without fetching environments or
// reading settings from disk.
func SelectEnvironment(available []environments.EnvironmentResource, mergedDefaultEnvironmentID string, provider SettingsProvider) Info {
	if len(available) == 0 {
		return Info{AvailableEnvironments: []environments.EnvironmentResource{}, SelectedEnvironment: nil, SelectedEnvironmentSource: nil}
	}

	availableCopy := append([]environments.EnvironmentResource(nil), available...)
	selected := defaultEnvironment(availableCopy)
	selectedSource := (*SettingSource)(nil)

	if mergedDefaultEnvironmentID != "" {
		if matching, ok := findEnvironmentByID(availableCopy, mergedDefaultEnvironmentID); ok {
			selected = matching
			if provider != nil {
				if source, ok := findSourceForDefault(provider, mergedDefaultEnvironmentID); ok {
					selectedSource = &source
				}
			}
		}
	}

	return Info{
		AvailableEnvironments:     availableCopy,
		SelectedEnvironment:       &selected,
		SelectedEnvironmentSource: selectedSource,
	}
}

// findSourceForDefault walks setting sources from highest to lowest priority and returns
// the first source whose defaultEnvironmentId matches the configured value, mirroring the
// TypeScript loop that skips flagSettings.
func findSourceForDefault(provider SettingsProvider, defaultEnvironmentID string) (SettingSource, bool) {
	for _, source := range slices.Backward(SettingSources) {
		if source == SourceFlag {
			// Skip flagSettings as it's not a normal source we check.
			continue
		}
		settings := provider(source)
		if settings.DefaultEnvironmentID == defaultEnvironmentID {
			return source, true
		}
	}
	return "", false
}

// defaultEnvironment mirrors the TypeScript fallback `environments.find(env => env.kind !== 'bridge') ?? environments[0]`.
func defaultEnvironment(available []environments.EnvironmentResource) environments.EnvironmentResource {
	for _, environment := range available {
		if environment.Kind != environments.KindBridge {
			return environment
		}
	}
	return available[0]
}

func findEnvironmentByID(available []environments.EnvironmentResource, id string) (environments.EnvironmentResource, bool) {
	for _, environment := range available {
		if environment.EnvironmentID == id {
			return environment, true
		}
	}
	return environments.EnvironmentResource{}, false
}
