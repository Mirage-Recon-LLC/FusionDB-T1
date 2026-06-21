//go:build embed_bin && darwin

package main

import _ "embed"

//go:embed fusiondb
var embeddedBinary []byte
