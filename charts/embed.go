// Package charts embeds the ergoz Helm chart so the CLI installs it without
// a checkout — the same pattern as sympozium's embedded chart.
package charts

import "embed"

// Ergoz is the embedded Helm chart filesystem (rooted at "ergoz/").
// The all: prefix is load-bearing: without it go:embed silently skips
// underscore-prefixed files — i.e. templates/_helpers.tpl.
//
//go:embed all:ergoz
var Ergoz embed.FS
