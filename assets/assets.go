// Package assets embeds static resources (icons) compiled into the binary so
// the app and tray share a single source of truth and need no files at runtime.
package assets

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

//go:embed icon_512px.png
var appIconPNG []byte

//go:embed icon_128px.png
var trayIconPNG []byte

// AppIcon is the embedded application/window icon. A large pre-rendered PNG is
// used (rather than the source SVG) so it stays crisp when the window manager
// or app switcher shows it at a larger size.
var AppIcon = fyne.NewStaticResource("icon_512px.png", appIconPNG)

// TrayIcon is the embedded system-tray icon. A smaller pre-rendered PNG keeps it
// crisp when the tray scales it down, and avoids shipping a needlessly large
// bitmap for a tiny target.
var TrayIcon = fyne.NewStaticResource("icon_128px.png", trayIconPNG)
