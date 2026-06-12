package git

import (
	"errors"
	"testing"
)

// TestValidateGitRef covers the flag-injection guard added for gu-n5dvk
// (security audit gu-nid89.10, finding #1). git parses any positional arg
// beginning with "-" as an option, so a ref named "--upload-pack=<cmd>" would
// change a command's behavior; validateGitRef closes that boundary.
func TestValidateGitRef(t *testing.T) {
	cases := []struct {
		name    string
		ref     string
		wantErr bool
	}{
		{"plain branch", "feature", false},
		{"namespaced branch", "polecat/foo/bar--mqa0001", false},
		{"remote ref", "origin/main", false},
		{"refspec with colon", "abc123:refs/heads/main", false},
		{"sha", "0123456789abcdef0123456789abcdef01234567", false},
		{"slashes and dots", "release/1.2.3", false},

		{"empty", "", true},
		{"leading dash short flag", "-x", true},
		{"leading double dash", "--force", true},
		{"upload-pack injection", "--upload-pack=touch /tmp/pwn", true},
		{"output injection", "--output=/etc/passwd", true},
		{"newline", "main\nrm -rf /", true},
		{"carriage return", "main\r", true},
		{"tab control char", "main\tfoo", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGitRef(tc.ref)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("validateGitRef(%q) = nil, want error", tc.ref)
				}
				if !errors.Is(err, ErrUnsafeGitRef) {
					t.Errorf("validateGitRef(%q) error = %v, want ErrUnsafeGitRef", tc.ref, err)
				}
			} else if err != nil {
				t.Errorf("validateGitRef(%q) = %v, want nil", tc.ref, err)
			}
		})
	}
}

func TestValidateGitRefs(t *testing.T) {
	if err := validateGitRefs("origin", "main"); err != nil {
		t.Errorf("validateGitRefs(origin, main) = %v, want nil", err)
	}
	// The first bad ref is reported.
	err := validateGitRefs("origin", "--upload-pack=x")
	if !errors.Is(err, ErrUnsafeGitRef) {
		t.Fatalf("validateGitRefs with malicious branch = %v, want ErrUnsafeGitRef", err)
	}
}

// TestGitWrappersRejectFlagInjection verifies that the ref-accepting wrappers
// reject an attacker-controlled "--upload-pack=" / leading-dash argument before
// it ever reaches git, and do so without running a subprocess. The malicious
// ref must surface ErrUnsafeGitRef, not a git "unknown option" error — proving
// the guard is at the wrapper boundary.
func TestGitWrappersRejectFlagInjection(t *testing.T) {
	g := NewGit(initTestRepo(t))
	const evil = "--upload-pack=touch /tmp/gt-pwned"

	wrappers := map[string]func() error{
		"Checkout":            func() error { return g.Checkout(evil) },
		"CheckoutNewBranch":   func() error { return g.CheckoutNewBranch(evil, "main") },
		"CheckoutResetBranch": func() error { return g.CheckoutResetBranch(evil, "main") },
		"CheckoutFileFromRef": func() error { return g.CheckoutFileFromRef(evil, "README.md") },
		"Fetch":               func() error { return g.Fetch(evil) },
		"FetchPrune":          func() error { return g.FetchPrune(evil) },
		"FetchBranch":         func() error { return g.FetchBranch("origin", evil) },
		"FetchBranchShallow":  func() error { return g.FetchBranchShallow("origin", evil) },
		"Pull":                func() error { return g.Pull("origin", evil) },
		"Push":                func() error { return g.Push("origin", evil, false) },
		"PushSkipPrePush":     func() error { return g.PushSkipPrePush("origin", evil, false) },
		"PushWithEnv":         func() error { return g.PushWithEnv("origin", evil, false, nil) },
		"PushSHA":             func() error { return g.PushSHA("origin", "deadbeef", evil, false) },
		"Merge":               func() error { return g.Merge(evil) },
		"MergeNoFF":           func() error { return g.MergeNoFF(evil, "msg") },
		"MergeFFOnly":         func() error { return g.MergeFFOnly(evil) },
		"MergeSquash":         func() error { return g.MergeSquash(evil, "msg") },
		"Rebase":              func() error { return g.Rebase(evil) },
		"RemoteBranchExists":  func() error { _, err := g.RemoteBranchExists("origin", evil); return err },
		"RemoteBranchTip":     func() error { _, err := g.RemoteBranchTip("origin", evil); return err },
		"MergeBase":           func() error { _, err := g.MergeBase("HEAD", evil); return err },
		"PushSubmoduleCommit": func() error { return g.PushSubmoduleCommit("sub", "deadbeef", evil) },
	}

	for name, fn := range wrappers {
		t.Run(name, func(t *testing.T) {
			err := fn()
			if err == nil {
				t.Fatalf("%s accepted malicious ref %q, want rejection", name, evil)
			}
			if !errors.Is(err, ErrUnsafeGitRef) {
				t.Errorf("%s rejected %q with %v, want ErrUnsafeGitRef", name, evil, err)
			}
		})
	}
}

// TestGitWrappersAcceptValidRefs is the regression guard: legitimate refs must
// still pass the validator and reach git unchanged.
func TestGitWrappersAcceptValidRefs(t *testing.T) {
	g := NewGit(initTestRepo(t))
	if err := g.CreateBranch("feature"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout("feature"); err != nil {
		t.Fatalf("Checkout(feature) = %v, want nil", err)
	}
	if err := g.Checkout("main"); err != nil {
		t.Fatalf("Checkout(main) = %v, want nil", err)
	}
	// DetachHead is the dedicated, validation-free path for the one legitimate
	// leading-"-" arg ("--detach") that previously rode through Checkout.
	if err := g.DetachHead(); err != nil {
		t.Fatalf("DetachHead() = %v, want nil", err)
	}
	detached, err := g.IsDetachedHEAD()
	if err != nil {
		t.Fatalf("IsDetachedHEAD: %v", err)
	}
	if !detached {
		t.Fatal("DetachHead() did not detach HEAD")
	}
}
