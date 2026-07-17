package environmentselection

import (
	"testing"

	"code-cli/internal/environments"
)

func TestSelectEnvironmentEmptyAvailable(t *testing.T) {
	info := SelectEnvironment(nil, "", nil)
	if len(info.AvailableEnvironments) != 0 || info.SelectedEnvironment != nil || info.SelectedEnvironmentSource != nil {
		t.Fatalf("info = %#v", info)
	}
}

func TestSelectEnvironmentDefaultsToFirstNonBridge(t *testing.T) {
	available := []environments.EnvironmentResource{
		{EnvironmentID: "bridge_1", Kind: environments.KindBridge, Name: "Bridge"},
		{EnvironmentID: "byoc_1", Kind: environments.KindBYOC, Name: "BYOC"},
		{EnvironmentID: "cloud_1", Kind: environments.KindAnthropicCloud, Name: "Cloud"},
	}
	info := SelectEnvironment(available, "", nil)
	if info.SelectedEnvironment == nil || info.SelectedEnvironment.EnvironmentID != "byoc_1" || info.SelectedEnvironmentSource != nil {
		t.Fatalf("info = %#v", info)
	}
}

func TestSelectEnvironmentFallsBackToFirstWhenOnlyBridges(t *testing.T) {
	available := []environments.EnvironmentResource{
		{EnvironmentID: "bridge_1", Kind: environments.KindBridge, Name: "Bridge"},
	}
	info := SelectEnvironment(available, "", nil)
	if info.SelectedEnvironment == nil || info.SelectedEnvironment.EnvironmentID != "bridge_1" || info.SelectedEnvironmentSource != nil {
		t.Fatalf("info = %#v", info)
	}
}

func TestSelectEnvironmentUsesMatchingDefaultEnvironment(t *testing.T) {
	available := []environments.EnvironmentResource{
		{EnvironmentID: "cloud_1", Kind: environments.KindAnthropicCloud, Name: "Cloud"},
		{EnvironmentID: "byoc_1", Kind: environments.KindBYOC, Name: "BYOC"},
	}
	info := SelectEnvironment(available, "byoc_1", nil)
	if info.SelectedEnvironment == nil || info.SelectedEnvironment.EnvironmentID != "byoc_1" || info.SelectedEnvironmentSource != nil {
		t.Fatalf("info = %#v", info)
	}
}

func TestSelectEnvironmentIgnoresUnknownDefaultEnvironment(t *testing.T) {
	available := []environments.EnvironmentResource{
		{EnvironmentID: "cloud_1", Kind: environments.KindAnthropicCloud, Name: "Cloud"},
	}
	info := SelectEnvironment(available, "missing", nil)
	if info.SelectedEnvironment == nil || info.SelectedEnvironment.EnvironmentID != "cloud_1" || info.SelectedEnvironmentSource != nil {
		t.Fatalf("info = %#v", info)
	}
}

func TestSelectEnvironmentReportsHighestPrioritySource(t *testing.T) {
	available := []environments.EnvironmentResource{
		{EnvironmentID: "cloud_1", Kind: environments.KindAnthropicCloud, Name: "Cloud"},
	}
	provider := func(source SettingSource) SourceSettings {
		switch source {
		case SourceUser, SourceProject, SourceLocal:
			return SourceSettings{DefaultEnvironmentID: "cloud_1"}
		default:
			return SourceSettings{}
		}
	}
	info := SelectEnvironment(available, "cloud_1", provider)
	if info.SelectedEnvironment == nil || info.SelectedEnvironment.EnvironmentID != "cloud_1" {
		t.Fatalf("environment = %#v", info.SelectedEnvironment)
	}
	if info.SelectedEnvironmentSource == nil || *info.SelectedEnvironmentSource != SourceLocal {
		t.Fatalf("source = %#v", info.SelectedEnvironmentSource)
	}
}

func TestSelectEnvironmentSkipsFlagSettingsSource(t *testing.T) {
	available := []environments.EnvironmentResource{
		{EnvironmentID: "cloud_1", Kind: environments.KindAnthropicCloud, Name: "Cloud"},
	}
	provider := func(source SettingSource) SourceSettings {
		if source == SourceFlag {
			return SourceSettings{DefaultEnvironmentID: "cloud_1"}
		}
		return SourceSettings{}
	}
	info := SelectEnvironment(available, "cloud_1", provider)
	if info.SelectedEnvironmentSource != nil {
		t.Fatalf("flag source was selected: %#v", info.SelectedEnvironmentSource)
	}
}

func TestSelectEnvironmentDoesNotMutateInput(t *testing.T) {
	available := []environments.EnvironmentResource{
		{EnvironmentID: "cloud_1", Kind: environments.KindAnthropicCloud, Name: "Cloud"},
		{EnvironmentID: "byoc_1", Kind: environments.KindBYOC, Name: "BYOC"},
	}
	info := SelectEnvironment(available, "byoc_1", nil)
	if available[0].EnvironmentID != "cloud_1" || len(available) != 2 {
		t.Fatalf("available was mutated: %#v", available)
	}
	info.AvailableEnvironments[0] = environments.EnvironmentResource{EnvironmentID: "mutated"}
	if available[0].EnvironmentID == "mutated" {
		t.Fatal("info.AvailableEnvironments aliases the caller slice")
	}
}

func TestSettingSourcesOrderMatchesTypeScript(t *testing.T) {
	want := []SettingSource{SourceUser, SourceProject, SourceLocal, SourceFlag, SourcePolicy}
	if len(SettingSources) != len(want) {
		t.Fatalf("length = %d", len(SettingSources))
	}
	for i, source := range SettingSources {
		if source != want[i] {
			t.Fatalf("SettingSources[%d] = %q, want %q", i, source, want[i])
		}
	}
}
