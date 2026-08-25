package tools

import (
	"context"
	"testing"
)

func TestEcho(t *testing.T) {
	output, err := Echo{}.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "hello" {
		t.Errorf("got %q, want %q", output, "hello")
	}
}
