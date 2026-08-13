package utils

import (
	"slices"
	"testing"
)

func TestAppendUnique(t *testing.T) {
	got := AppendUnique(nil, "a")
	got = AppendUnique(got, "b")
	got = AppendUnique(got, "a")
	want := []string{"a", "b"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
