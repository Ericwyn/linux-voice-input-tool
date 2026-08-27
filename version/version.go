// Package version holds the single source of truth for the application's
// version string, shared by the CLI (--version, --help) and the settings UI.
package version

// Version is the current application version, following semantic versioning.
const Version = "0.6.0"

// Name is the human-facing application name used in help and window titles.
const Name = "Linux Voice Input"

// String renders the name and version together, e.g. "Linux Voice Input 0.1.0".
func String() string {
	return Name + " " + Version
}
