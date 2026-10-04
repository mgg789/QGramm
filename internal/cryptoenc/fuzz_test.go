package cryptoenc

import (
	"bytes"
	"encoding/json"
	"testing"
)

// Exercise untrusted frames and HPKE envelope parsing through the real primitives.
func FuzzEncryptedFrames(f *testing.F) {
	e, _ := New(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	frame, _ := e.Seal([]byte("seed"), []byte("binding"))
	f.Add(frame)
	f.Add([]byte(`{"key_id":"invalid","enc":"?","ciphertext":"?"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 65536 {
			t.Skip()
		}
		_, _ = e.Open(raw, []byte("binding"))
		var envelope Envelope
		if json.Unmarshal(raw, &envelope) == nil {
			_, _ = e.OpenEnvelope(envelope, []byte("binding"))
		}
	})
}
