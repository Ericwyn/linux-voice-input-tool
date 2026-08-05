// Package assets embeds static resources (icons) compiled into the binary so
// the app and tray share a single source of truth and need no files at runtime.
package assets

import _ "embed"

//go:embed icon_512px.png
var appIconPNG []byte

//go:embed icon_128px.png
var trayIconPNG []byte

// AppIconPNG is the embedded application/window icon as raw PNG bytes. A large
// pre-rendered PNG is used (rather than the source SVG) so it stays crisp when
// scaled up.
var AppIconPNG = appIconPNG

// TrayIconPNG is the embedded system-tray icon as raw PNG bytes. gogpu/systray
// consumes PNG bytes directly via SetIcon. A smaller pre-rendered PNG keeps it
// crisp when the tray scales it down.
var TrayIconPNG = trayIconPNG
