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

func TestContainsInt64(t *testing.T) {
	if ContainsInt64(nil, 1) || !ContainsInt64([]int64{2, 1}, 1) {
		t.Fatal("ContainsInt64")
	}
}

func TestUniqueInt64(t *testing.T) {
	got := UniqueInt64([]int64{3, 1, 3, 2, 1})
	want := []int64{3, 1, 2}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
