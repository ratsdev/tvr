package epg

import (
	"strings"
	"testing"
)

func TestNaturalCompareLatinFirst(t *testing.T) {
	names := []string{"天津卫视", "CCTV-10", "CCTV-2", "CCTV-1", "风云音乐", "Alpha"}
	// insertion sort via pairwise checks for clarity
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if naturalLess(names[j], names[i]) {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	want := []string{"Alpha", "CCTV-1", "CCTV-2", "CCTV-10", "天津卫视", "风云音乐"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", names, want)
	}
}
