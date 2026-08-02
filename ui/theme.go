package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// boldTheme wraps the default fyne theme and renders regular (upright,
// proportional) text with the bold font. The stock font weight looks thin and
// slightly blurry on some Linux displays; forcing bold improves legibility
// while leaving colors, sizes, icons, and monospace/italic text untouched.
type boldTheme struct {
	fyne.Theme
}

// newBoldTheme builds the bold-text theme on top of the current default theme,
// so it still follows the system light/dark variant.
func newBoldTheme() fyne.Theme {
	return boldTheme{Theme: theme.DefaultTheme()}
}

// Font returns the bold font for ordinary text and defers to the wrapped theme
// for monospace and italic styles, where forcing bold would be inappropriate.
func (b boldTheme) Font(style fyne.TextStyle) fyne.Resource {
	if !style.Monospace && !style.Italic {
		style.Bold = true
	}
	return b.Theme.Font(style)
}
