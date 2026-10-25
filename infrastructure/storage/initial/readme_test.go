package initial

import "testing"

func TestReadme(t *testing.T) {
	if got := string(Readme("My Project")); got != "# My Project\n" {
		t.Fatalf("got %q", got)
	}
}
