package main

import "testing"

func TestValidateBatchReply(t *testing.T) {
	for _, test := range []struct {
		name, body     string
		minimal, valid bool
	}{
		{"full_partial", `[{"operation_id":"a","status":201,"message":{"id":"m","chat_id":"chat","operation_id":"a","seq":1}},{"operation_id":"b","status":409}]`, false, true},
		{"minimal_partial", `[{"operation_id":"a","status":201,"receipt":{"message_id":"m","chat_id":"chat","operation_id":"a","seq":1}},{"operation_id":"b","status":503}]`, true, true},
		{"wrong_chat", `[{"operation_id":"a","status":201,"message":{"id":"m","chat_id":"other","operation_id":"a","seq":1}},{"operation_id":"b","status":409}]`, false, false},
		{"missing_receipt", `[{"operation_id":"a","status":201},{"operation_id":"b","status":409}]`, true, false},
		{"duplicate_result", `[{"operation_id":"a","status":409},{"operation_id":"a","status":409}]`, false, false},
		{"truncated", `[{"operation_id":"a","status":409}]`, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			statuses, err := validateBatchReply([]byte(test.body), []string{"a", "b"}, "chat", test.minimal)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
			if test.valid && (len(statuses) != 2 || statuses[0] != 201) {
				t.Fatal(statuses)
			}
		})
	}
}
