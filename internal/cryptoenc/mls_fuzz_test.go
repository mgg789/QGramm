//go:build qg_e2ee

package cryptoenc

import "testing"

func FuzzMLSKeyPackage(f *testing.F) {
	participant, err := NewMLS([]byte("fuzz-device"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(participant.KeyPackage())
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, wire []byte) {
		if len(wire) > 65536 {
			t.Skip()
		}
		_, _, _ = KeyPackageIdentity(wire)
	})
}
