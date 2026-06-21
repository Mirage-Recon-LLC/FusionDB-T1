//go:build embed_bin && windows

package main

import _ "embed"

//go:embed fusiondb.exe
var embeddedBinary []byte
