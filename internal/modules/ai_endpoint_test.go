//go:build qg_ai_endpoint && qg_e2ee && qg_groups && qg_delete && qg_edit && qg_reactions && qg_reply && qg_forward

package modules

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestAIEndpointRegistryAuthorizationAndIdentityOwnership(t *testing.T) {
	h := newModuleHarness(t, "global")
	if _, err := h.c.DB.Exec(`UPDATE devices SET signing_key=? WHERE id='bob-phone'`, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))); err != nil {
		t.Fatal(err)
	}
	body := map[string]string{"user_id": "bob", "device_id": "bob-phone", "kind": "llm"}
	path := "/management/v1/ai/endpoints/local-model"
	h.request(t, "PUT", path, body, 401, false)
	h.request(t, "PUT", path, body, 200, true)
	h.request(t, "PUT", path, body, 200, true)
	changed := map[string]string{"user_id": "bob", "device_id": "bob-phone", "kind": "tools"}
	h.request(t, "PUT", path, changed, 409, true)
	h.request(t, "PUT", "/management/v1/ai/endpoints/duplicate", body, 409, true)
	if result := h.request(t, "GET", "/v1/chats/chat/ai/endpoints", nil, 200, false); !bytes.Contains(result, []byte("local-model")) || !bytes.Contains(result, []byte(`"participant_owner":"endpoint"`)) {
		t.Fatal("registered endpoint missing", string(result))
	}
	if _, err := h.c.DB.Exec(`UPDATE devices SET revoked=1 WHERE id='bob-phone'`); err != nil {
		t.Fatal(err)
	}
	h.request(t, "PUT", path, body, 403, true)
	if result := h.request(t, "GET", "/v1/chats/chat/ai/endpoints", nil, 200, false); bytes.Contains(result, []byte("local-model")) {
		t.Fatal("revoked endpoint exposed")
	}
	if _, err := h.c.DB.Exec(`UPDATE members SET active=0 WHERE user_id='alice'`); err != nil {
		t.Fatal(err)
	}
	h.request(t, "GET", "/v1/chats/chat/ai/endpoints", nil, 403, false)
}
