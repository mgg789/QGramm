//go:build qg_ai_endpoint && qg_e2ee

package microsafer

import (
	"bytes"
	"encoding/json"
	"testing"
)

func FuzzRPCJSON(f *testing.F) {
	f.Add([]byte(`{"version":1,"request_id":"request","client_id":"client","chat":"chat","source_peer":"device","action":"call","name":"tool","body":{"query":"test"}}`))
	f.Add([]byte(`{"query":"first","query":"second"}`))
	f.Add([]byte(`{"nested":[null,1,true,"text"]}`))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 65536 {
			return
		}
		canonical, err := CanonicalJSON(input)
		if err == nil {
			if !json.Valid(canonical) {
				t.Fatal("canonicalization returned invalid JSON")
			}
			again, err := CanonicalJSON(canonical)
			if err != nil || !bytes.Equal(canonical, again) {
				t.Fatal("canonicalization is not stable")
			}
			a, _ := RequestHash(input)
			b, _ := RequestHash(canonical)
			if !bytes.Equal(a, b) {
				t.Fatal("canonical-equivalent input changed request hash")
			}
		}
		var request RPCRequest
		if decodeStrictJSON(input, &request) == nil {
			_ = request.Validate(65536)
			_, _ = request.Hash()
		}
	})
}
