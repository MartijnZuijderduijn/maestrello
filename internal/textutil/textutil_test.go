package textutil

import (
	"testing"
)

func TestFirstLineAndTail(t *testing.T) {
	if got := FirstLine("one\ntwo", 10); got != "one" {
		t.Errorf("firstLine = %q", got)
	}
	if got := FirstLine("abcdefgh", 3); got != "abc…" {
		t.Errorf("firstLine truncation = %q", got)
	}
	if got := Tail("  hello world  ", 5); got != "…world" {
		t.Errorf("tail = %q", got)
	}
	if got := Tail("short", 10); got != "short" {
		t.Errorf("tail short = %q", got)
	}
}
