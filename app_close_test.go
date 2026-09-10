package main

import "testing"

// TestDecideClose pins the whole close-guard policy. It is pure, so it needs
// no window — which matters, because every other part of this feature can only
// be exercised on Windows.
//
// The three "allow" rows are the ones that keep warpseed closable at all. A
// guard that can refuse forever is a worse bug than no guard.
func TestDecideClose(t *testing.T) {
	cases := []struct {
		name             string
		quitting, pendng bool
		running          int
		action           string
		want             closeDecision
	}{
		{"quit re-entrancy: runtime.Quit's second pass must fall through",
			true, false, 5, "ask", closeAllow},
		{"a second close gesture is the user insisting",
			false, true, 5, "ask", closeAllow},
		{"an idle app closes instantly, exactly as before",
			false, false, 0, "ask", closeAllow},
		{"idle beats the preference: pill must not make the X useless",
			false, false, 0, "pill", closeAllow},
		{"transfers running, asking",
			false, false, 3, "ask", closeAsk},
		{"transfers running, told to just close",
			false, false, 3, "quit", closeAllow},
		{"transfers running, told to shrink to the pill",
			false, false, 3, "pill", closePill},
		{"an unset preference asks rather than acting",
			false, false, 3, "", closeAsk},
		{"a value from a newer build asks rather than guessing",
			false, false, 3, "obliterate", closeAsk},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideClose(tc.quitting, tc.pendng, tc.running, tc.action)
			if got != tc.want {
				t.Fatalf("decideClose(%v, %v, %d, %q) = %v, want %v",
					tc.quitting, tc.pendng, tc.running, tc.action, got, tc.want)
			}
		})
	}
}

// TestCloseGuardAlwaysHasAnExit is the property the individual rows imply but
// do not state: whatever the preference, SOME sequence of gestures closes the
// app. A user must never be left with a window they cannot shut.
func TestCloseGuardAlwaysHasAnExit(t *testing.T) {
	for _, action := range []string{"ask", "quit", "pill", "", "nonsense"} {
		t.Run(action, func(t *testing.T) {
			// First gesture, with transfers running: may be vetoed.
			first := decideClose(false, false, 4, action)
			if first == closeAllow {
				return // already closable
			}
			// Second gesture, with the guard outstanding, must always allow.
			if got := decideClose(false, true, 4, action); got != closeAllow {
				t.Fatalf("a second close gesture gave %v, want closeAllow — "+
					"the window would be unclosable", got)
			}
		})
	}
}
