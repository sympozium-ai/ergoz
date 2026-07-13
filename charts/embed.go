// Package charts embeds the ergoz Helm chart so the CLI installs it without
// a checkout — the same pattern as sympozium's embedded chart.
package charts

import "embed"

// Ergoz is the embedded Helm chart filesystem (rooted at "ergoz/").
//
//go:embed ergoz
var Ergoz embed.FS
