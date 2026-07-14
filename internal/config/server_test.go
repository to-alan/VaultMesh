package config

import "testing"

func TestSplitListAndValidateOrigin(t *testing.T) {
	origins := splitList(" https://console.example.com, http://127.0.0.1:5173 ,, ")
	if len(origins) != 2 || origins[0] != "https://console.example.com" || origins[1] != "http://127.0.0.1:5173" {
		t.Fatalf("unexpected origins: %#v", origins)
	}
	for _, origin := range origins {
		if err := validateOrigin(origin); err != nil {
			t.Fatalf("expected %q to be valid: %v", origin, err)
		}
	}
	for _, origin := range []string{"*", "javascript:alert(1)", "https://example.com/", "https://example.com/path", "https://user@example.com"} {
		if err := validateOrigin(origin); err == nil {
			t.Fatalf("expected %q to be invalid", origin)
		}
	}
}
