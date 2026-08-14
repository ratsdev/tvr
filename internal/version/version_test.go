package version

import "testing"

func TestShortCommitAndLabel(t *testing.T) {
	origV, origC := Version, Commit
	t.Cleanup(func() {
		Version, Commit = origV, origC
	})

	Version = "dev"
	Commit = "abcdef1234567890"
	if got := ShortCommit(); got != "abcdef1" {
		t.Fatalf("ShortCommit=%q", got)
	}
	if got := Label(); got != "dev(abcdef1)" {
		t.Fatalf("dev Label=%q", got)
	}

	Version = "1.2.3"
	if got := Label(); got != "1.2.3(abcdef1)" {
		t.Fatalf("Label=%q", got)
	}

	Commit = "abc"
	if got := Label(); got != "1.2.3(abc)" {
		t.Fatalf("short commit Label=%q", got)
	}

	Commit = ""
	if got := Label(); got != "1.2.3" {
		t.Fatalf("no commit Label=%q", got)
	}
}
