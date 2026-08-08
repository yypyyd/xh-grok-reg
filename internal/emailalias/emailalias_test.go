package emailalias

import "testing"

func TestAddress(t *testing.T) {
	if got := Address("user@example.com", "001"); got != "user+001@example.com" {
		t.Fatalf("Address() = %q, want %q", got, "user+001@example.com")
	}
}

func TestBase(t *testing.T) {
	tests := []struct {
		address string
		want    string
	}{
		{"user+001@example.com", "user@example.com"},
		{"user-001@example.com", "user-001@example.com"},
		{"user+tag@example.com", "user+tag@example.com"},
	}
	for _, tt := range tests {
		if got := Base(tt.address); got != tt.want {
			t.Errorf("Base(%q) = %q, want %q", tt.address, got, tt.want)
		}
	}
}

func TestLikePattern(t *testing.T) {
	if got := LikePattern("user@example.com"); got != "user+%@example.com" {
		t.Fatalf("LikePattern() = %q, want %q", got, "user+%@example.com")
	}
}
