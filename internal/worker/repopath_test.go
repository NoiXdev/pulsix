package worker

import "testing"

func TestCleanRepoPath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{".", ""},
		{"/", ""},
		{"ansible/", "ansible"},
		{"/ansible", "ansible"},
		{"a//b", "a/b"},
		{"./a/./b", "a/b"},
		{"../x", "../x"},
		{"a/../..", ".."},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := CleanRepoPath(tc.in); got != tc.want {
				t.Errorf("CleanRepoPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
