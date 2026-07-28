package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

type palette struct {
	promptBorder          color.Color
	text                  color.Color
	inactive              color.Color
	subtle                color.Color
	userMessageBackground color.Color
	claude                color.Color
	clawdBackground       color.Color
	error                 color.Color
}

func newPalette(dark bool, profile colorprofile.Profile) palette {
	lightDark := lipgloss.LightDark(dark)
	return palette{
		promptBorder:          completeColor(profile, ansi.White, lightDark(lipgloss.Color("#999999"), lipgloss.Color("#888888"))),
		text:                  completeColor(profile, lightDark(ansi.Black, ansi.BrightWhite), lightDark(lipgloss.Color("#000000"), lipgloss.Color("#ffffff"))),
		inactive:              completeColor(profile, lightDark(ansi.BrightBlack, ansi.White), lightDark(lipgloss.Color("#666666"), lipgloss.Color("#999999"))),
		subtle:                completeColor(profile, lightDark(ansi.BrightBlack, ansi.White), lightDark(lipgloss.Color("#afafaf"), lipgloss.Color("#505050"))),
		userMessageBackground: completeColor(profile, lightDark(ansi.White, ansi.BrightBlack), lightDark(lipgloss.Color("#f0f0f0"), lipgloss.Color("#373737"))),
		// Source clawd_body / claude orange.
		claude: completeColor(profile, ansi.BrightRed, lipgloss.Color("#d77757")),
		// Source clawd_background is always black so the eye block reads as pupils.
		clawdBackground: completeColor(profile, ansi.Black, lipgloss.Color("#000000")),
		// Source dark theme error is rgb(255,107,128); light is rgb(171,43,63).
		error: completeColor(profile, ansi.Red, lightDark(lipgloss.Color("#ab2b3f"), lipgloss.Color("#ff6b80"))),
	}
}

func completeColor(profile colorprofile.Profile, basic, rgb color.Color) color.Color {
	return lipgloss.Complete(profile)(basic, colorprofile.ANSI256.Convert(rgb), rgb)
}
