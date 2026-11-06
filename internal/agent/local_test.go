package agent

import "testing"

func TestLocalMaskingAfterWipeMasksEverything(t *testing.T) {
	lm := NewLocalMasking(map[string]map[string]string{"secret://t/x": {"": "hunter2-value"}})
	if got := lm.Mask("a hunter2-value b"); got != "a *** b" {
		t.Fatalf("Mask = %q", got)
	}
	lm.Wipe()
	if got := lm.Mask("late line"); got != "***" {
		t.Fatalf("Mask after Wipe = %q", got)
	}
	if _, err := lm.Resolver().Resolve("secret://t/x"); err == nil {
		t.Fatal("resolver still serves a wiped secret")
	}
}
