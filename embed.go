// Package ergoz embeds the deploy manifests so the CLI installer ships them
// inside the binary — `ergoz install` needs no checkout, mirroring
// sympozium's embedded-chart installer.
package ergoz

import _ "embed"

// DeployManifest is the canonical agent+collector deployment (deploy/ergoz.yaml).
//
//go:embed deploy/ergoz.yaml
var DeployManifest []byte
