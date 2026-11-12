package jenkins

import (
	"strings"
	"testing"
)

func TestJobPathTopLevel(t *testing.T) {
	p, err := JobPath("ModuleSubmitBuild")
	if err != nil {
		t.Fatalf("JobPath: %v", err)
	}
	if want := "/job/ModuleSubmitBuild"; p != want {
		t.Fatalf("got %q, want %q", p, want)
	}
}

func TestJobPathFolders(t *testing.T) {
	p, err := JobPath("team/sub/build-job")
	if err != nil {
		t.Fatalf("JobPath: %v", err)
	}
	if want := "/job/team/job/sub/job/build-job"; p != want {
		t.Fatalf("got %q, want %q", p, want)
	}
}

func TestJobPathRejectsTraversal(t *testing.T) {
	for _, in := range []string{"", "/", "a//b", "a/../b", "..", ".", "a/./b"} {
		if _, err := JobPath(in); err == nil {
			t.Errorf("JobPath(%q) should have failed", in)
		}
	}
}

func TestJobPathEscapesSegments(t *testing.T) {
	p, err := JobPath("a b/c+d")
	if err != nil {
		t.Fatalf("JobPath: %v", err)
	}
	// A space must not survive into the URL. A '+' is a literal character in a
	// path segment (unlike in a query string), so escaping it would be wrong.
	if strings.Contains(p, " ") {
		t.Fatalf("space was not escaped: %q", p)
	}
	if want := "/job/a%20b/job/c+d"; p != want {
		t.Fatalf("got %q, want %q", p, want)
	}
}

func TestParseBuildRef(t *testing.T) {
	ok := []string{"1", "31249", "lastBuild", "lastSuccessfulBuild", "lastFailedBuild", "lastCompletedBuild"}
	for _, s := range ok {
		if _, err := ParseBuildRef(s); err != nil {
			t.Errorf("ParseBuildRef(%q) failed: %v", s, err)
		}
	}
	bad := []string{"", "0", "-1", "latest", "last", "1.5", "../../etc"}
	for _, s := range bad {
		if _, err := ParseBuildRef(s); err == nil {
			t.Errorf("ParseBuildRef(%q) should have failed", s)
		}
	}
}
