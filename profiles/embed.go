// Package profiles embeds the built-in organization profiles.
// Profiles are data, not code (SPEC §4): a manifest, JSON Schemas per note
// type, and templates. `pkms profile eject <name> <dir>` copies one out.
package profiles

import "embed"

//go:embed para rdegges
var FS embed.FS
