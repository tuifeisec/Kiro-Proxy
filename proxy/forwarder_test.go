package proxy

import "testing"

func TestIsPreviousResponseRejected(t *testing.T) {
	cases := []string{
		`{"error":{"code":"previous_response_not_found","message":"previous_response_id not found"}}`,
		`{"error":{"message":"Unknown parameter: previous_response_id"}}`,
		`{"error":{"message":"invalid previous response id"}}`,
		`{"error":{"message":"previous_response_id is unsupported"}}`,
	}
	for _, body := range cases {
		if !isPreviousResponseRejected(body) {
			t.Fatalf("should reject: %s", body)
		}
	}

	if isPreviousResponseRejected(`{"error":{"message":"invalid api key"}}`) {
		t.Fatal("unrelated error should not be treated as previous_response_id rejection")
	}
}
