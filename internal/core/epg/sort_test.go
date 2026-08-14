package epg

import (
	"strings"
	"testing"

	"github.com/jqjiang/tvr/internal/utils"
)

func TestNaturalCompareLatinFirst(t *testing.T) {
	names := []string{"天津卫视", "CCTV-10", "CCTV-2", "CCTV-1", "风云音乐", "Alpha"}
	// insertion sort via pairwise checks for clarity
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if utils.NaturalLess(names[j], names[i]) {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	want := []string{"Alpha", "CCTV-1", "CCTV-2", "CCTV-10", "天津卫视", "风云音乐"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", names, want)
	}
}

func TestIndexFromDocFollowsGuideOrder(t *testing.T) {
	doc := &tvDocument{Channels: map[string]tvChannel{
		"tj":  {ID: "tj", DisplayNames: []tvText{{Text: "天津卫视"}}},
		"c10": {ID: "c10", DisplayNames: []tvText{{Text: "CCTV-10"}}},
		"c2":  {ID: "c2", DisplayNames: []tvText{{Text: "CCTV-2"}}},
		"c1":  {ID: "c1", DisplayNames: []tvText{{Text: "CCTV-1"}}},
		"fy":  {ID: "fy", DisplayNames: []tvText{{Text: "风云音乐"}}},
		"a":   {ID: "a", DisplayNames: []tvText{{Text: "Alpha"}}},
	}}
	got := indexFromDoc(doc)
	want := []string{"Alpha", "CCTV-1", "CCTV-2", "CCTV-10", "天津卫视", "风云音乐"}
	names := make([]string, len(got))
	for i, ch := range got {
		names[i] = ch.DisplayNames[0]
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", names, want)
	}
}

func TestChannelSearchLessExactIDFirst(t *testing.T) {
	exact := ChannelInfo{ID: "cnn", DisplayNames: []string{"ZZZ"}}
	other := ChannelInfo{ID: "cnn-headline", DisplayNames: []string{"CNN Headline"}}
	if !channelSearchLess("cnn", exact, other) || channelSearchLess("cnn", other, exact) {
		t.Fatal("exact id should rank first")
	}
}

func TestPageChannelSearchCapsResults(t *testing.T) {
	small := []ChannelInfo{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got := pageChannelSearch(small, 50)
	if got.Total != 3 || len(got.Channels) != 3 {
		t.Fatalf("small: %+v", got)
	}

	large := make([]ChannelInfo, 80)
	for i := range large {
		large[i] = ChannelInfo{ID: string(rune('a' + (i % 26)))}
	}
	got = pageChannelSearch(large, 50)
	if got.Total != 80 || len(got.Channels) != 50 {
		t.Fatalf("paged: total=%d n=%d", got.Total, len(got.Channels))
	}
}
