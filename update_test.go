package main

import "testing"

// TestNewerVersion pins the two answers that matter: never miss a real update,
// and never offer a downgrade to a build that is ahead of the latest release.
func TestNewerVersion(t *testing.T) {
	cases := []struct {
		name            string
		latest, current string
		want            bool
	}{
		{"a newer patch is an update", "1.1.9", "1.1.8", true},
		{"a newer minor is an update", "1.2.0", "1.1.9", true},
		{"a newer major is an update", "2.0.0", "1.9.9", true},
		{"the same version is not", "1.1.8", "1.1.8", false},
		{"an older release is never offered", "1.1.7", "1.1.8", false},
		// Live case: the tree is bumped in the commit that ships it, so
		// between a bump and its tag every dev build is ahead of the latest
		// release. It must stay quiet rather than offer a downgrade.
		{"a dev build ahead of the latest release", "1.1.7", "1.1.8", false},
		{"leading v on the tag is ignored", "v1.1.9", "1.1.8", true},
		{"a prerelease suffix compares on the numbers", "1.2.0-rc1", "1.1.9", true},
		{"differing component counts still compare", "1.2", "1.1.9", true},
		{"trailing zero component is not an update", "1.2.0", "1.2", false},
		// Anything unparseable must be treated as "say nothing".
		{"garbage latest is silent", "not-a-version", "1.1.8", false},
		{"garbage current is silent", "1.1.9", "", false},
		{"empty latest is silent", "", "1.1.8", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := newerVersion(tc.latest, tc.current); got != tc.want {
				t.Fatalf("newerVersion(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
			}
		})
	}
}
