package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---- validSlug -------------------------------------------------------------

func TestValidSlug(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"app-489", true},
		{"my-feature", true},
		{"a", true},
		{"a.b", true},
		{"a_b", true},
		{"abc123", true},

		{"", false},
		{"-leading-dash", false},
		{".leading-dot", false},
		{"UPPER", false},
		{"with space", false},
		{"with/slash", false},
	}
	for _, c := range cases {
		if got := validSlug(c.in); got != c.want {
			t.Errorf("validSlug(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// ---- task round-trip + findTaskRoot ---------------------------------------

func TestTaskRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := task{Ref: "app-1", Slug: "demo"}
	if err := saveTask(dir, in); err != nil {
		t.Fatal(err)
	}
	got, err := loadTask(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, in)
	}

	// ref is omitted when empty
	dir2 := t.TempDir()
	if err := saveTask(dir2, task{Slug: "noref"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir2, metaFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ref:") {
		t.Errorf("expected no ref: line in %s, got:\n%s", metaFile, raw)
	}
}

func TestLoadTaskRequiresSlug(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, metaFile), []byte("ref: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTask(dir); err == nil {
		t.Error("expected error for missing slug")
	}
}

func TestFindTaskRoot(t *testing.T) {
	root := t.TempDir()
	if err := saveTask(root, task{Slug: "demo"}); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := findTaskRoot(deep)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Errorf("findTaskRoot from %s = %s, want %s", deep, got, root)
	}

	// no task anywhere
	noTask := t.TempDir()
	if _, err := findTaskRoot(noTask); err == nil {
		t.Error("expected error when no .tsk.yaml in any parent")
	}
}

// ---- isInside --------------------------------------------------------------

func TestIsInside(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "x", "y")
	if !isInside(child, parent) {
		t.Errorf("expected %s to be inside %s", child, parent)
	}
	other := t.TempDir()
	if isInside(other, parent) {
		t.Errorf("expected %s NOT to be inside %s", other, parent)
	}
}

// ---- git helpers (against tmp repos) ---------------------------------------

// makeBareRepo creates a bare repo and a working clone with one commit on
// `main`. Returns (bareDir, sourceDir).
func makeRepoPair(t *testing.T) (bare, src string) {
	t.Helper()
	bare = filepath.Join(t.TempDir(), "remote.git")
	if err := exec.Command("git", "init", "--bare", "-b", "main", bare).Run(); err != nil {
		t.Fatal(err)
	}
	src = filepath.Join(t.TempDir(), "src")
	if err := exec.Command("git", "clone", "-b", "main", bare, src).Run(); err != nil {
		// older git fallback
		if err2 := exec.Command("git", "clone", bare, src).Run(); err2 != nil {
			t.Fatalf("clone failed: %v / %v", err, err2)
		}
	}
	mustRunGit(t, src, "config", "user.email", "test@example.com")
	mustRunGit(t, src, "config", "user.name", "Test")
	mustRunGit(t, src, "checkout", "-B", "main")
	if err := os.WriteFile(filepath.Join(src, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, src, "add", ".")
	mustRunGit(t, src, "commit", "-m", "init")
	mustRunGit(t, src, "push", "-u", "origin", "main")
	return bare, src
}

func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := runGit(dir, args...); err != nil {
		t.Fatalf("git %s in %s: %v", strings.Join(args, " "), dir, err)
	}
}

func TestGitBranchExists(t *testing.T) {
	_, src := makeRepoPair(t)
	if got, _ := gitBranchExists(src, "main"); !got {
		t.Error("expected main to exist")
	}
	if got, _ := gitBranchExists(src, "missing"); got {
		t.Error("expected 'missing' branch not to exist")
	}
}

func TestSourceRepoOf_AndIsWorktree(t *testing.T) {
	_, src := makeRepoPair(t)
	wtDir := filepath.Join(t.TempDir(), "wt")
	if _, err := runGit(src, "worktree", "add", "-b", "feat", wtDir, "main"); err != nil {
		t.Fatal(err)
	}
	if !isWorktree(wtDir) {
		t.Errorf("expected %s to be a worktree", wtDir)
	}
	got, err := sourceRepoOf(wtDir)
	if err != nil {
		t.Fatal(err)
	}
	wantSrc, _ := filepath.EvalSymlinks(src)
	gotEval, _ := filepath.EvalSymlinks(got)
	if gotEval != wantSrc {
		t.Errorf("sourceRepoOf = %s, want %s", gotEval, wantSrc)
	}
}

// ---- end-to-end command tests ---------------------------------------------

// runIn executes `fn` with cwd set to dir, restoring it afterwards.
func runIn(t *testing.T, dir string, fn func()) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(prev) }()
	fn()
}

func TestCmdCreate_WithRef(t *testing.T) {
	dir := t.TempDir()
	runIn(t, dir, func() {
		if err := cmdCreate([]string{"app-489", "my-feature"}); err != nil {
			t.Fatal(err)
		}
	})
	taskDir := filepath.Join(dir, "app-489-my-feature")
	if _, err := os.Stat(filepath.Join(taskDir, metaFile)); err != nil {
		t.Fatalf("expected %s to exist: %v", metaFile, err)
	}
	got, err := loadTask(taskDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ref != "app-489" || got.Slug != "my-feature" {
		t.Errorf("unexpected task: %+v", got)
	}
}

func TestCmdCreate_NoRef(t *testing.T) {
	dir := t.TempDir()
	runIn(t, dir, func() {
		if err := cmdCreate([]string{"only-slug"}); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Stat(filepath.Join(dir, "only-slug", metaFile)); err != nil {
		t.Fatalf("expected task dir: %v", err)
	}
}

func TestCmdCreate_RejectsExisting(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "dup"), 0o755); err != nil {
		t.Fatal(err)
	}
	runIn(t, dir, func() {
		if err := cmdCreate([]string{"dup"}); err == nil {
			t.Error("expected error when target dir exists")
		}
	})
}

func TestCmdCreate_RejectsBadSlug(t *testing.T) {
	dir := t.TempDir()
	runIn(t, dir, func() {
		if err := cmdCreate([]string{"BadSlug"}); err == nil {
			t.Error("expected error for invalid slug")
		}
	})
}

func TestCmdCreate_WithAddFlag(t *testing.T) {
	_, src := makeRepoPair(t)

	tasks := t.TempDir()
	runIn(t, tasks, func() {
		if err := cmdCreate([]string{"feat", "-a", src}); err != nil {
			t.Fatal(err)
		}
	})

	taskDir := filepath.Join(tasks, "feat")
	wt := filepath.Join(taskDir, filepath.Base(src))
	if !isWorktree(wt) {
		t.Errorf("expected worktree at %s", wt)
	}
	br, _ := runGit(wt, "branch", "--show-current")
	if br != "feat" {
		t.Errorf("worktree on branch %q, want feat", br)
	}
}

func TestCmdAdd_HappyPath(t *testing.T) {
	_, src := makeRepoPair(t)

	tasks := t.TempDir()
	runIn(t, tasks, func() {
		if err := cmdCreate([]string{"feat"}); err != nil {
			t.Fatal(err)
		}
	})
	taskDir := filepath.Join(tasks, "feat")

	runIn(t, taskDir, func() {
		if err := cmdAdd([]string{src}); err != nil {
			t.Fatal(err)
		}
	})

	wt := filepath.Join(taskDir, filepath.Base(src))
	if !isWorktree(wt) {
		t.Errorf("expected worktree at %s", wt)
	}
	br, _ := runGit(wt, "branch", "--show-current")
	if br != "feat" {
		t.Errorf("worktree on branch %q, want feat", br)
	}
}

func TestCmdAdd_DuplicateBranchFails(t *testing.T) {
	_, src := makeRepoPair(t)
	mustRunGit(t, src, "branch", "feat") // create branch ahead of time

	tasks := t.TempDir()
	runIn(t, tasks, func() {
		if err := cmdCreate([]string{"feat"}); err != nil {
			t.Fatal(err)
		}
	})
	taskDir := filepath.Join(tasks, "feat")
	runIn(t, taskDir, func() {
		if err := cmdAdd([]string{src}); err == nil {
			t.Error("expected error: branch already exists")
		}
	})
}

func TestCmdAdd_CustomBranch(t *testing.T) {
	_, src := makeRepoPair(t)

	tasks := t.TempDir()
	runIn(t, tasks, func() {
		if err := cmdCreate([]string{"feat"}); err != nil {
			t.Fatal(err)
		}
	})
	taskDir := filepath.Join(tasks, "feat")
	runIn(t, taskDir, func() {
		if err := cmdAdd([]string{"-b", "alt", src}); err != nil {
			t.Fatal(err)
		}
	})
	wt := filepath.Join(taskDir, filepath.Base(src))
	br, _ := runGit(wt, "branch", "--show-current")
	if br != "alt" {
		t.Errorf("branch = %q, want alt", br)
	}
}

func TestCmdAdd_BaseBranch(t *testing.T) {
	_, src := makeRepoPair(t)

	// Create a `develop` branch with an extra commit, push it, then
	// delete the local copy so we can prove the worktree was built from
	// `origin/develop` (not a local ref).
	mustRunGit(t, src, "checkout", "-b", "develop")
	if err := os.WriteFile(filepath.Join(src, "DEV"), []byte("dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, src, "add", ".")
	mustRunGit(t, src, "commit", "-m", "dev commit")
	mustRunGit(t, src, "push", "-u", "origin", "develop")
	mustRunGit(t, src, "checkout", "main")
	mustRunGit(t, src, "branch", "-D", "develop")

	tasks := t.TempDir()
	runIn(t, tasks, func() {
		if err := cmdCreate([]string{"feat"}); err != nil {
			t.Fatal(err)
		}
	})
	taskDir := filepath.Join(tasks, "feat")
	runIn(t, taskDir, func() {
		if err := cmdAdd([]string{"--base", "origin/develop", src}); err != nil {
			t.Fatal(err)
		}
	})

	wt := filepath.Join(taskDir, filepath.Base(src))
	if _, err := os.Stat(filepath.Join(wt, "DEV")); err != nil {
		t.Errorf("expected DEV file (from develop) in worktree: %v", err)
	}
	br, _ := runGit(wt, "branch", "--show-current")
	if br != "feat" {
		t.Errorf("branch = %q, want feat", br)
	}
}

func TestCmdAdd_BaseRejectsMissingSlash(t *testing.T) {
	_, src := makeRepoPair(t)

	tasks := t.TempDir()
	runIn(t, tasks, func() {
		if err := cmdCreate([]string{"feat"}); err != nil {
			t.Fatal(err)
		}
	})
	taskDir := filepath.Join(tasks, "feat")
	runIn(t, taskDir, func() {
		err := cmdAdd([]string{"--base", "main", src})
		if err == nil {
			t.Fatal("expected error: --base main is missing a remote prefix")
		}
		if !strings.Contains(err.Error(), "<remote>/<branch>") {
			t.Errorf("error should explain expected format, got: %v", err)
		}
	})
}

// TestCmdAdd_DefaultBaseUsesFirstRemote builds a repo whose only remote is
// named "upstream" (not "origin") and whose default branch is "master" (not
// "main"). Without --base, tsk should resolve the base via upstream/master.
func TestCmdAdd_DefaultBaseUsesFirstRemote(t *testing.T) {
	bare := filepath.Join(t.TempDir(), "remote.git")
	if err := exec.Command("git", "init", "--bare", "-b", "master", bare).Run(); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "src")
	if err := exec.Command("git", "clone", "-o", "upstream", bare, src).Run(); err != nil {
		t.Fatalf("clone failed: %v", err)
	}
	mustRunGit(t, src, "config", "user.email", "test@example.com")
	mustRunGit(t, src, "config", "user.name", "Test")
	mustRunGit(t, src, "checkout", "-B", "master")
	if err := os.WriteFile(filepath.Join(src, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, src, "add", ".")
	mustRunGit(t, src, "commit", "-m", "init")
	mustRunGit(t, src, "push", "-u", "upstream", "master")
	// `git push -u` doesn't set the remote HEAD on a clone of a previously-empty
	// bare repo, so seed it explicitly — defaultBase falls back to ls-remote if
	// this is missing, but we want to exercise the cheap symbolic-ref path too.
	mustRunGit(t, src, "remote", "set-head", "upstream", "master")

	tasks := t.TempDir()
	runIn(t, tasks, func() {
		if err := cmdCreate([]string{"feat"}); err != nil {
			t.Fatal(err)
		}
	})
	taskDir := filepath.Join(tasks, "feat")
	runIn(t, taskDir, func() {
		if err := cmdAdd([]string{src}); err != nil {
			t.Fatalf("default base detection failed: %v", err)
		}
	})

	wt := filepath.Join(taskDir, filepath.Base(src))
	if !isWorktree(wt) {
		t.Errorf("expected worktree at %s", wt)
	}
	br, _ := runGit(wt, "branch", "--show-current")
	if br != "feat" {
		t.Errorf("branch = %q, want feat", br)
	}
}

func TestParseRemoteBranch(t *testing.T) {
	cases := []struct {
		in           string
		wantRemote   string
		wantBranch   string
		wantOK       bool
	}{
		{"origin/main", "origin", "main", true},
		{"upstream/master", "upstream", "master", true},
		{"origin/feature/foo", "origin", "feature/foo", true},
		{"main", "", "", false},
		{"/main", "", "", false},
		{"origin/", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		r, b, ok := parseRemoteBranch(c.in)
		if r != c.wantRemote || b != c.wantBranch || ok != c.wantOK {
			t.Errorf("parseRemoteBranch(%q) = (%q,%q,%v); want (%q,%q,%v)",
				c.in, r, b, ok, c.wantRemote, c.wantBranch, c.wantOK)
		}
	}
}

func TestCmdRm(t *testing.T) {
	_, src := makeRepoPair(t)

	tasks := t.TempDir()
	runIn(t, tasks, func() {
		if err := cmdCreate([]string{"feat"}); err != nil {
			t.Fatal(err)
		}
	})
	taskDir := filepath.Join(tasks, "feat")
	runIn(t, taskDir, func() {
		if err := cmdAdd([]string{src}); err != nil {
			t.Fatal(err)
		}
		if err := cmdRm([]string{filepath.Base(src)}); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Stat(filepath.Join(taskDir, filepath.Base(src))); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("worktree dir should be gone: err=%v", err)
	}
}

func TestCmdRm_RejectsNonWorktree(t *testing.T) {
	tasks := t.TempDir()
	runIn(t, tasks, func() {
		if err := cmdCreate([]string{"feat"}); err != nil {
			t.Fatal(err)
		}
	})
	taskDir := filepath.Join(tasks, "feat")
	junk := filepath.Join(taskDir, "not-a-worktree")
	if err := os.Mkdir(junk, 0o755); err != nil {
		t.Fatal(err)
	}
	runIn(t, taskDir, func() {
		if err := cmdRm([]string{"not-a-worktree"}); err == nil {
			t.Error("expected error: not a worktree")
		}
	})
}

func TestCmdClose_BlocksOnUnpushedThenForceWorks(t *testing.T) {
	_, src := makeRepoPair(t)

	tasks := t.TempDir()
	runIn(t, tasks, func() {
		if err := cmdCreate([]string{"feat"}); err != nil {
			t.Fatal(err)
		}
	})
	taskDir := filepath.Join(tasks, "feat")
	runIn(t, taskDir, func() {
		if err := cmdAdd([]string{src}); err != nil {
			t.Fatal(err)
		}
	})

	// Without --force: must fail because branch has no upstream yet.
	if err := cmdClose([]string{taskDir}); err == nil {
		t.Error("expected close to fail without --force on unpushed branch")
	} else if !strings.Contains(err.Error(), "upstream") && !strings.Contains(err.Error(), "unpushed") {
		t.Errorf("unexpected error message: %v", err)
	}

	// With --force: should succeed and clean up.
	if err := cmdClose([]string{"--force", taskDir}); err != nil {
		t.Fatalf("force close failed: %v", err)
	}
	if _, err := os.Stat(taskDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("task dir should be gone: err=%v", err)
	}
	// Source repo should not have a leftover prunable worktree entry.
	out, err := runGit(src, "worktree", "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, taskDir) {
		t.Errorf("unexpected leftover worktree entry:\n%s", out)
	}
}

func TestCmdClose_SucceedsWhenPushed(t *testing.T) {
	_, src := makeRepoPair(t)

	tasks := t.TempDir()
	runIn(t, tasks, func() {
		if err := cmdCreate([]string{"feat"}); err != nil {
			t.Fatal(err)
		}
	})
	taskDir := filepath.Join(tasks, "feat")
	runIn(t, taskDir, func() {
		if err := cmdAdd([]string{src}); err != nil {
			t.Fatal(err)
		}
	})

	wt := filepath.Join(taskDir, filepath.Base(src))
	mustRunGit(t, wt, "config", "user.email", "test@example.com")
	mustRunGit(t, wt, "config", "user.name", "Test")
	mustRunGit(t, wt, "push", "-u", "origin", "feat")

	if err := cmdClose([]string{taskDir}); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if _, err := os.Stat(taskDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("task dir should be gone: err=%v", err)
	}
}

func TestCmdClose_BlocksOnDirty(t *testing.T) {
	_, src := makeRepoPair(t)

	tasks := t.TempDir()
	runIn(t, tasks, func() {
		if err := cmdCreate([]string{"feat"}); err != nil {
			t.Fatal(err)
		}
	})
	taskDir := filepath.Join(tasks, "feat")
	runIn(t, taskDir, func() {
		if err := cmdAdd([]string{src}); err != nil {
			t.Fatal(err)
		}
	})

	wt := filepath.Join(taskDir, filepath.Base(src))
	mustRunGit(t, wt, "config", "user.email", "test@example.com")
	mustRunGit(t, wt, "config", "user.name", "Test")
	mustRunGit(t, wt, "push", "-u", "origin", "feat")

	// Make the worktree dirty.
	if err := os.WriteFile(filepath.Join(wt, "x.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := cmdClose([]string{taskDir})
	if err == nil {
		t.Fatal("expected dirty error")
	}
	if !strings.Contains(err.Error(), "dirty") {
		t.Errorf("unexpected error: %v", err)
	}
}
