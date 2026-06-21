//go:build embed_bin && linux

package main

import _ "embed"

//go:embed fusiondb
var embeddedBinary []byte
