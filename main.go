// tsk — multi-repo task workspaces backed by git worktrees.
//
// One task is one directory containing a .tsk.yaml marker and a git worktree
// for each repo the task touches. There is no global config and no central
// tasks directory: tasks live wherever the user runs `tsk create`.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

const version = "0.1.0"

const remote = "origin"
const defaultBase = "main"

const metaFile = ".tsk.yaml"

// task is the on-disk schema for .tsk.yaml.
type task struct {
	Ref  string `yaml:"ref,omitempty"`
	Slug string `yaml:"slug"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tsk: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage(os.Stderr)
		return errors.New("missing command")
	}
	switch args[0] {
	case "create":
		return cmdCreate(args[1:])
	case "add":
		return cmdAdd(args[1:])
	case "status":
		return cmdStatus(args[1:])
	case "rm":
		return cmdRm(args[1:])
	case "close":
		return cmdClose(args[1:])
	case "-h", "--help", "help":
		usage(os.Stdout)
		return nil
	case "-v", "--version":
		fmt.Println("tsk", version)
		return nil
	default:
		usage(os.Stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `tsk — multi-repo task workspaces

usage:
  tsk create [<ref>] <slug> [--from <branch>] [-a <repo-path> ...]
                                     Create a task directory in cwd
  tsk add <repo-path> [<repo-path> ...] [-b <branch>] [--from <branch>]
                                     Add worktrees to the current task
  tsk status                         git status summary across all worktrees
  tsk rm [-f] <repo-path>            Remove one worktree from the current task
  tsk close [-f] <task-path>         Decommission a task: clean worktrees + delete dir

  tsk --version                      Print version
  tsk --help                         Print this help
`)
}

// ---- cmdCreate -------------------------------------------------------------

func cmdCreate(args []string) error {
	// -a is variadic, so pull it out before flag parsing.
	var addPaths []string
	mainArgs := args
	for i, a := range args {
		if a == "-a" {
			mainArgs = args[:i]
			addPaths = args[i+1:]
			break
		}
	}

	flags := flag.NewFlagSet("create", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	from := flags.String("from", defaultBase, "remote branch to base new branches on (used with -a)")
	if err := flags.Parse(mainArgs); err != nil {
		return err
	}
	rest := flags.Args()

	var ref, slug string
	switch len(rest) {
	case 1:
		slug = rest[0]
	case 2:
		ref, slug = rest[0], rest[1]
	default:
		return errors.New("usage: tsk create [<ref>] <slug> [--from <branch>] [-a <repo-path> ...]")
	}

	if ref != "" && !validSlug(ref) {
		return fmt.Errorf("invalid ref %q (must match [a-z0-9][a-z0-9._-]*)", ref)
	}
	if !validSlug(slug) {
		return fmt.Errorf("invalid slug %q (must match [a-z0-9][a-z0-9._-]*)", slug)
	}

	dirName := slug
	if ref != "" {
		dirName = ref + "-" + slug
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	taskDir := filepath.Join(cwd, dirName)

	if _, err := os.Stat(taskDir); err == nil {
		return fmt.Errorf("%s already exists", taskDir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	fmt.Printf("creating task %s\n", dirName)
	if err := os.Mkdir(taskDir, 0o755); err != nil {
		return err
	}

	if err := saveTask(taskDir, task{Ref: ref, Slug: slug}); err != nil {
		// best-effort cleanup
		_ = os.RemoveAll(taskDir)
		return err
	}

	fmt.Println(taskDir)

	for _, p := range addPaths {
		if err := addOne(taskDir, p, slug, *from); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}

	return nil
}

// ---- cmdAdd ----------------------------------------------------------------

func cmdAdd(args []string) error {
	flags := flag.NewFlagSet("add", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	branch := flags.String("b", "", "branch name to create (defaults to task slug)")
	from := flags.String("from", defaultBase, "remote branch to base the new branch on")
	if err := flags.Parse(args); err != nil {
		return err
	}
	repos := flags.Args()
	if len(repos) == 0 {
		return errors.New("usage: tsk add <repo-path> [<repo-path> ...] [-b <branch>] [--from <branch>]")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	taskRoot, err := findTaskRoot(cwd)
	if err != nil {
		return err
	}
	t, err := loadTask(taskRoot)
	if err != nil {
		return err
	}

	chosenBranch := *branch
	if chosenBranch == "" {
		chosenBranch = t.Slug
	}

	for _, p := range repos {
		if err := addOne(taskRoot, p, chosenBranch, *from); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

func addOne(taskRoot, repoPath, branch, base string) error {
	if base == "" {
		base = defaultBase
	}
	src, err := filepath.Abs(repoPath)
	if err != nil {
		return err
	}
	if _, err := runGit(src, "rev-parse", "--git-dir"); err != nil {
		return fmt.Errorf("not a git repo: %s", src)
	}

	name := filepath.Base(src)
	dest := filepath.Join(taskRoot, name)
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("worktree dir %q already exists in task", name)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	fmt.Printf("fetching %s/%s for %s...\n", remote, base, name)
	if _, err := runGit(src, "fetch", remote, base); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	exists, err := gitBranchExists(src, branch)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("branch %q already exists in source repo (pass -b to pick another)", branch)
	}

	fmt.Printf("creating worktree %s [%s]...\n", name, branch)
	// `-c branch.autoSetupMerge=false` keeps the new branch from inheriting
	// the base branch as its upstream — we want "never pushed" to remain
	// detectable until the user actually pushes it.
	if _, err := runGit(src,
		"-c", "branch.autoSetupMerge=false",
		"worktree", "add", "-b", branch, dest, remote+"/"+base,
	); err != nil {
		return err
	}
	fmt.Printf("added %s → %s [%s]\n", src, dest, branch)
	return nil
}

// ---- cmdStatus -------------------------------------------------------------

func cmdStatus(args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	if err := flags.Parse(args); err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	taskRoot, err := findTaskRoot(cwd)
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(taskRoot)
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	any := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wt := filepath.Join(taskRoot, e.Name())
		if !isWorktree(wt) {
			continue
		}
		any = true

		br, _ := runGit(wt, "branch", "--show-current")
		modified, ahead, behind, err := worktreeStatus(wt)
		if err != nil {
			fmt.Fprintf(tw, "%s\t[%s]\terror: %v\n", e.Name(), br, err)
			continue
		}

		state := "clean"
		if modified > 0 {
			state = fmt.Sprintf("M %d", modified)
		}
		track := ""
		if ahead > 0 {
			track += fmt.Sprintf("↑%d ", ahead)
		}
		if behind > 0 {
			track += fmt.Sprintf("↓%d", behind)
		}
		track = strings.TrimSpace(track)
		fmt.Fprintf(tw, "%s\t[%s]\t%s\t%s\n", e.Name(), br, state, track)
	}
	if !any {
		fmt.Fprintln(os.Stdout, "(no worktrees in this task)")
		return nil
	}
	return tw.Flush()
}

// ---- cmdRm -----------------------------------------------------------------

func cmdRm(args []string) error {
	flags := flag.NewFlagSet("rm", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	force := flags.Bool("f", false, "force removal even if dirty")
	flags.BoolVar(force, "force", false, "force removal even if dirty")
	if err := flags.Parse(args); err != nil {
		return err
	}
	rest := flags.Args()
	if len(rest) != 1 {
		return errors.New("usage: tsk rm [-f] <repo-path>")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	taskRoot, err := findTaskRoot(cwd)
	if err != nil {
		return err
	}

	wt, err := filepath.Abs(rest[0])
	if err != nil {
		return err
	}
	if !isInside(wt, taskRoot) {
		return fmt.Errorf("%s is not inside the current task (%s)", wt, taskRoot)
	}
	if !isWorktree(wt) {
		return fmt.Errorf("%s is not a git worktree", wt)
	}

	src, err := sourceRepoOf(wt)
	if err != nil {
		return err
	}

	rmArgs := []string{"worktree", "remove"}
	if *force {
		rmArgs = append(rmArgs, "--force")
	}
	rmArgs = append(rmArgs, wt)
	if _, err := runGit(src, rmArgs...); err != nil {
		return err
	}
	fmt.Printf("removed %s (source %s)\n", wt, src)
	return nil
}

// ---- cmdClose --------------------------------------------------------------

func cmdClose(args []string) error {
	flags := flag.NewFlagSet("close", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	force := flags.Bool("f", false, "force close: skip dirty/unpushed safety checks")
	flags.BoolVar(force, "force", false, "force close: skip dirty/unpushed safety checks")
	if err := flags.Parse(args); err != nil {
		return err
	}
	rest := flags.Args()
	if len(rest) != 1 {
		return errors.New("usage: tsk close [-f] <task-path>")
	}

	taskRoot, err := filepath.Abs(rest[0])
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(taskRoot, metaFile)); err != nil {
		return fmt.Errorf("not a task directory (no %s in %s)", metaFile, taskRoot)
	}

	entries, err := os.ReadDir(taskRoot)
	if err != nil {
		return err
	}

	type wtInfo struct {
		path   string
		src    string
		branch string
	}
	var worktrees []wtInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wt := filepath.Join(taskRoot, e.Name())
		if !isWorktree(wt) {
			continue
		}
		src, err := sourceRepoOf(wt)
		if err != nil {
			return fmt.Errorf("%s: %w", wt, err)
		}
		br, err := runGit(wt, "branch", "--show-current")
		if err != nil {
			return fmt.Errorf("%s: %w", wt, err)
		}
		worktrees = append(worktrees, wtInfo{path: wt, src: src, branch: br})
	}

	if !*force {
		var problems []string
		for _, w := range worktrees {
			if dirty, err := runGit(w.path, "status", "--porcelain"); err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", w.path, err))
			} else if dirty != "" {
				problems = append(problems, fmt.Sprintf("%s: dirty working tree", w.path))
			}

			// Refresh remote tracking before checking upstream / ahead count.
			_, _ = runGit(w.src, "fetch", remote, w.branch)

			up, _ := runGit(w.path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
			if up == "" {
				problems = append(problems, fmt.Sprintf("%s: branch %q has no upstream (never pushed)", w.path, w.branch))
				continue
			}
			aheadStr, err := runGit(w.path, "rev-list", "--count", "@{u}..HEAD")
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", w.path, err))
				continue
			}
			ahead, _ := strconv.Atoi(strings.TrimSpace(aheadStr))
			if ahead > 0 {
				problems = append(problems, fmt.Sprintf("%s: branch %q has %d unpushed commit(s)", w.path, w.branch, ahead))
			}
		}
		if len(problems) > 0 {
			sort.Strings(problems)
			return fmt.Errorf("close blocked (use --force to override):\n  - %s", strings.Join(problems, "\n  - "))
		}
	}

	for _, w := range worktrees {
		rmArgs := []string{"worktree", "remove"}
		if *force {
			rmArgs = append(rmArgs, "--force")
		}
		rmArgs = append(rmArgs, w.path)
		if _, err := runGit(w.src, rmArgs...); err != nil {
			return fmt.Errorf("removing %s: %w", w.path, err)
		}
		fmt.Printf("removed %s (source %s)\n", w.path, w.src)
	}

	if err := os.RemoveAll(taskRoot); err != nil {
		return err
	}
	fmt.Printf("closed %s\n", taskRoot)
	return nil
}

// ---- task metadata ---------------------------------------------------------

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func validSlug(s string) bool { return slugRe.MatchString(s) }

func findTaskRoot(start string) (string, error) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, metaFile)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not inside a task (no %s found in any parent)", metaFile)
		}
		dir = parent
	}
}

func loadTask(dir string) (task, error) {
	data, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return task{}, err
	}
	var t task
	if err := yaml.Unmarshal(data, &t); err != nil {
		return task{}, fmt.Errorf("parse %s: %w", metaFile, err)
	}
	if t.Slug == "" {
		return task{}, fmt.Errorf("%s missing slug", metaFile)
	}
	return t, nil
}

func saveTask(dir string, t task) error {
	data, err := yaml.Marshal(t)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, metaFile), data, 0o644)
}

// ---- git helpers -----------------------------------------------------------

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

func gitBranchExists(repo, branch string) (bool, error) {
	_, err := runGit(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	// `--quiet` makes failure exit-1 without stderr; treat any error as "doesn't exist".
	return false, nil
}

// isWorktree reports whether dir is a git worktree (working tree with a .git
// file or directory and a resolvable git-dir).
func isWorktree(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return false
	}
	_, err := runGit(dir, "rev-parse", "--git-dir")
	return err == nil
}

// sourceRepoOf returns the absolute path of the repo that owns the given
// worktree (i.e. the directory whose `.git/` the worktree links into).
func sourceRepoOf(worktree string) (string, error) {
	out, err := runGit(worktree, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(worktree, out)
	}
	return filepath.Dir(out), nil
}

// worktreeStatus returns counts of changed files (staged+unstaged+untracked) and
// ahead/behind vs upstream. Missing upstream → ahead=0, behind=0 with no error.
func worktreeStatus(wt string) (modified, ahead, behind int, err error) {
	out, err := runGit(wt, "status", "--porcelain=v1", "--branch")
	if err != nil {
		return 0, 0, 0, err
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			ahead, behind = parseAheadBehind(line)
			continue
		}
		modified++
	}
	return modified, ahead, behind, nil
}

var aheadRe = regexp.MustCompile(`ahead (\d+)`)
var behindRe = regexp.MustCompile(`behind (\d+)`)

func parseAheadBehind(line string) (int, int) {
	a, b := 0, 0
	if m := aheadRe.FindStringSubmatch(line); m != nil {
		a, _ = strconv.Atoi(m[1])
	}
	if m := behindRe.FindStringSubmatch(line); m != nil {
		b, _ = strconv.Atoi(m[1])
	}
	return a, b
}

// isInside reports whether child resolves to a path under parent.
func isInside(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
