package fusiondb

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("failed to generate test secret: " + err.Error())
	}
	os.Setenv("FUSIONDB_SECRET", hex.EncodeToString(key))
	os.Exit(m.Run())
}
