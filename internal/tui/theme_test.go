package tui

import (
	"reflect"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestPaletteTrueColorMatchesSourceTokens(t *testing.T) {
	tests := []struct {
		name string
		dark bool
		want palette
	}{
		{
			name: "light",
			want: palette{
				promptBorder:          lipgloss.Color("#999999"),
				text:                  lipgloss.Color("#000000"),
				inactive:              lipgloss.Color("#666666"),
				subtle:                lipgloss.Color("#afafaf"),
				userMessageBackground: lipgloss.Color("#f0f0f0"),
				claude:                lipgloss.Color("#d77757"),
			},
		},
		{
			name: "dark",
			dark: true,
			want: palette{
				promptBorder:          lipgloss.Color("#888888"),
				text:                  lipgloss.Color("#ffffff"),
				inactive:              lipgloss.Color("#999999"),
				subtle:                lipgloss.Color("#505050"),
				userMessageBackground: lipgloss.Color("#373737"),
				claude:                lipgloss.Color("#d77757"),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := newPalette(test.dark, colorprofile.TrueColor); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("newPalette() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestPaletteANSIFallbacks(t *testing.T) {
	light := newPalette(false, colorprofile.ANSI)
	if light.promptBorder != ansi.White || light.text != ansi.Black || light.inactive != ansi.BrightBlack || light.subtle != ansi.BrightBlack || light.userMessageBackground != ansi.White || light.claude != ansi.BrightRed {
		t.Fatalf("light ANSI palette = %#v", light)
	}

	dark := newPalette(true, colorprofile.ANSI)
	if dark.promptBorder != ansi.White || dark.text != ansi.BrightWhite || dark.inactive != ansi.White || dark.subtle != ansi.White || dark.userMessageBackground != ansi.BrightBlack || dark.claude != ansi.BrightRed {
		t.Fatalf("dark ANSI palette = %#v", dark)
	}
}

func TestPaletteANSI256UsesConvertedSourceColors(t *testing.T) {
	got := newPalette(true, colorprofile.ANSI256)
	want := palette{
		promptBorder:          colorprofile.ANSI256.Convert(lipgloss.Color("#888888")),
		text:                  colorprofile.ANSI256.Convert(lipgloss.Color("#ffffff")),
		inactive:              colorprofile.ANSI256.Convert(lipgloss.Color("#999999")),
		subtle:                colorprofile.ANSI256.Convert(lipgloss.Color("#505050")),
		userMessageBackground: colorprofile.ANSI256.Convert(lipgloss.Color("#373737")),
		claude:                colorprofile.ANSI256.Convert(lipgloss.Color("#d77757")),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("newPalette() = %#v, want %#v", got, want)
	}
}

func TestPaletteDisablesColorForASCIIProfile(t *testing.T) {
	noColor := lipgloss.NoColor{}
	want := palette{
		promptBorder:          noColor,
		text:                  noColor,
		inactive:              noColor,
		subtle:                noColor,
		userMessageBackground: noColor,
		claude:                noColor,
	}
	if got := newPalette(true, colorprofile.ASCII); !reflect.DeepEqual(got, want) {
		t.Fatalf("newPalette() = %#v, want %#v", got, want)
	}
}
