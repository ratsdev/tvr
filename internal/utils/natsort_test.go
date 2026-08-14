package utils_test

import (
	"slices"
	"testing"

	"github.com/ratsdev/tvr/internal/utils"
)

func TestCompareCorpus(t *testing.T) {
	in := []string{
		"CCTV-10",
		"CCTV-01",
		"CCTV-2",
		"CCTV-1",
		"alpha",
		"中文频道",
		"Beta",
		"湖南卫视",
	}
	want := []string{
		"alpha",
		"Beta",
		"CCTV-1",
		"CCTV-01",
		"CCTV-2",
		"CCTV-10",
		"中文频道",
		"湖南卫视",
	}
	got := append([]string(nil), in...)
	slices.SortStableFunc(got, utils.NaturalCompare)
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
