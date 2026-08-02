// Package assets embeds static resources (icons) compiled into the binary so
// the app and tray share a single source of truth and need no files at runtime.
package assets

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

//go:embed icon_64px.png
var iconPNG []byte

// AppIcon is the embedded application and system-tray icon. A pre-rendered PNG
// is used (rather than the source SVG) so it stays crisp when the tray scales
// it down to a small size.
var AppIcon = fyne.NewStaticResource("icon_64px.png", iconPNG)
