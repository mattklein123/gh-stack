package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/github/gh-stack/internal/config"
	"github.com/github/gh-stack/internal/stack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type submoduleStackRepo struct {
	dir, gitDir, parentDir, childDir string
	oldPin, newPin, stackPin         string
	originalRefs                     map[string]string
}

func setupSubmoduleStack(t *testing.T, linked, pinCommit, conflict bool) submoduleStackRepo {
	t.Helper()
	root := t.TempDir()
	module := filepath.Join(root, "module")
	require.NoError(t, os.Mkdir(module, 0755))
	issue250Git(t, module, "init", "-b", "main")
	for _, content := range []string{"old\n", "new\n", "stack\n"} {
		issue250WriteFile(t, module, "module.txt", content)
		issue250Git(t, module, "add", ".")
		issue250Git(t, module, "commit", "-m", content)
	}
	oldPin := issue250Git(t, module, "rev-parse", "HEAD~2")
	newPin := issue250Git(t, module, "rev-parse", "HEAD~1")
	stackPin := issue250Git(t, module, "rev-parse", "HEAD")

	remote := filepath.Join(root, "remote.git")
	dir := filepath.Join(root, "clone")
	issue250Git(t, root, "-c", "safe.bareRepository=all", "init", "--bare", "-b", "main", remote)
	issue250Git(t, root, "clone", remote, dir)
	issue250Git(t, dir, "config", "user.name", "Test")
	issue250Git(t, dir, "config", "user.email", "test@example.com")
	issue250Git(t, dir, "config", "commit.gpgSign", "false")
	issue250Git(t, dir, "config", "core.hooksPath", os.DevNull)
	issue250Git(t, dir, "config", "rerere.enabled", "true")
	issue250Git(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", module, "module with spaces")
	moduleDir := filepath.Join(dir, "module with spaces")
	issue250Git(t, moduleDir, "checkout", oldPin)
	issue250WriteFile(t, dir, "base.txt", "base\n")
	issue250Git(t, dir, "add", ".")
	issue250Git(t, dir, "commit", "-m", "base")
	main := issue250Git(t, dir, "rev-parse", "HEAD")
	issue250Git(t, dir, "checkout", "-b", "parent")
	issue250WriteFile(t, dir, "parent.txt", "parent\n")
	if conflict {
		issue250WriteFile(t, dir, "base.txt", "parent\n")
	}
	issue250Git(t, dir, "add", ".")
	issue250Git(t, dir, "commit", "-m", "parent")
	parent := issue250Git(t, dir, "rev-parse", "HEAD")
	issue250Git(t, dir, "checkout", "-b", "child")
	if pinCommit {
		issue250Git(t, moduleDir, "checkout", stackPin)
		issue250Git(t, dir, "add", "module with spaces")
		issue250Git(t, dir, "commit", "-m", "advance stack submodule")
	} else {
		issue250WriteFile(t, dir, "child.txt", "child\n")
		issue250Git(t, dir, "add", ".")
		issue250Git(t, dir, "commit", "-m", "child")
	}
	child := issue250Git(t, dir, "rev-parse", "HEAD")
	issue250Git(t, dir, "checkout", "main")
	issue250Git(t, moduleDir, "checkout", newPin)
	if conflict {
		issue250WriteFile(t, dir, "base.txt", "trunk\n")
	}
	issue250Git(t, dir, "add", ".")
	issue250Git(t, dir, "commit", "-m", "advance trunk submodule")
	issue250Git(t, dir, "push", "origin", "main", "parent", "child")
	issue250Git(t, dir, "checkout", "child")
	issue250Git(t, moduleDir, "checkout", oldPin)
	if pinCommit {
		issue250Git(t, moduleDir, "checkout", stackPin)
	}
	gitDir := filepath.Join(dir, ".git")
	writeStackFile(t, gitDir, stack.Stack{
		Trunk: stack.BranchRef{Branch: "main", Head: main},
		Branches: []stack.BranchRef{
			{Branch: "parent", Head: parent, Base: main},
			{Branch: "child", Head: child, Base: parent},
		},
	})
	repo := submoduleStackRepo{
		dir: dir, gitDir: gitDir, parentDir: dir, childDir: dir,
		oldPin: oldPin, newPin: newPin, stackPin: stackPin,
		originalRefs: map[string]string{"parent": parent, "child": child},
	}
	if linked {
		issue250Git(t, dir, "checkout", "main")
		issue250Git(t, moduleDir, "checkout", newPin)
		repo.parentDir = filepath.Join(root, "parent worktree")
		repo.childDir = filepath.Join(root, "child worktree")
		for branch, path := range map[string]string{"parent": repo.parentDir, "child": repo.childDir} {
			issue250Git(t, dir, "worktree", "add", path, branch)
			issue250Git(t, path, "-c", "protocol.file.allow=always", "submodule", "update", "--init")
		}
	}
	return repo
}

func TestSubmoduleStack_TrunkUpdate(t *testing.T) {
	for _, command := range []string{"rebase", "sync"} {
		for _, linked := range []bool{false, true} {
			for _, pinCommit := range []bool{false, true} {
				name := command
				if linked {
					name += "/linked"
				} else {
					name += "/single"
				}
				if pinCommit {
					name += "/pin-commit"
				}
				t.Run(name, func(t *testing.T) {
					repo := setupSubmoduleStack(t, linked, pinCommit, false)
					withIssue250Repo(t, repo.childDir)
					cfg := issue250TestConfig(t)
					require.NoError(t, runSubmoduleStackCommand(cfg, command))
					require.NoError(t, issue250GitMayFail(t, repo.dir, "merge-base", "--is-ancestor", "main", "parent"))
					require.NoError(t, issue250GitMayFail(t, repo.dir, "merge-base", "--is-ancestor", "parent", "child"))
					assert.Equal(t, "child", issue250Git(t, repo.childDir, "branch", "--show-current"))
					for _, path := range []string{repo.parentDir, repo.childDir} {
						assert.Empty(t, issue250Git(t, path, "status", "--porcelain", "--ignore-submodules=none"))
					}
					wantPin := repo.newPin
					if pinCommit {
						wantPin = repo.stackPin
						assert.Equal(t, "advance stack submodule", issue250Git(t, repo.dir, "log", "--format=%s", "parent..child"))
					}
					assert.Equal(t, wantPin, issue250Git(t, filepath.Join(repo.childDir, "module with spaces"), "rev-parse", "HEAD"))
					assert.NoFileExists(t, filepath.Join(repo.gitDir, rebaseStateFile))
					if command == "sync" {
						for _, branch := range []string{"parent", "child"} {
							assert.Equal(t, issue250Git(t, repo.dir, "rev-parse", branch), issue250Git(t, repo.dir, "rev-parse", "origin/"+branch))
						}
					}
				})
			}
		}
	}
}

func TestSubmoduleStack_Recovery(t *testing.T) {
	for _, action := range []string{"continue", "abort", "sync"} {
		for _, linked := range []bool{false, true} {
			name := action + "/single"
			if linked {
				name = action + "/linked"
			}
			t.Run(name, func(t *testing.T) {
				repo := setupSubmoduleStack(t, linked, false, true)
				withIssue250Repo(t, repo.childDir)
				cfg := issue250TestConfig(t)
				command := "rebase"
				if action == "sync" {
					command = "sync"
				}
				require.ErrorIs(t, runSubmoduleStackCommand(cfg, command), ErrConflict)
				if action != "sync" {
					assert.FileExists(t, filepath.Join(repo.gitDir, rebaseStateFile))
					assert.Equal(t, repo.newPin, issue250Git(t, filepath.Join(repo.parentDir, "module with spaces"), "rev-parse", "HEAD"))
					if action == "continue" {
						issue250WriteFile(t, repo.parentDir, "base.txt", "resolved\n")
						issue250Git(t, repo.parentDir, "add", "base.txt")
					}
					require.NoError(t, runRebase(cfg, &rebaseOptions{cont: action == "continue", abort: action == "abort"}))
				}
				wantPin := repo.oldPin
				if action == "continue" {
					wantPin = repo.newPin
					require.NoError(t, issue250GitMayFail(t, repo.dir, "merge-base", "--is-ancestor", "main", "parent"))
					require.NoError(t, issue250GitMayFail(t, repo.dir, "merge-base", "--is-ancestor", "parent", "child"))
				} else {
					for branch, sha := range repo.originalRefs {
						assert.Equal(t, sha, issue250Git(t, repo.dir, "rev-parse", branch))
					}
				}
				for _, path := range []string{repo.parentDir, repo.childDir} {
					assert.Empty(t, issue250Git(t, path, "status", "--porcelain", "--ignore-submodules=none"))
					assert.Equal(t, wantPin, issue250Git(t, filepath.Join(path, "module with spaces"), "rev-parse", "HEAD"))
				}
				assert.Equal(t, "child", issue250Git(t, repo.childDir, "branch", "--show-current"))
				assert.NoFileExists(t, filepath.Join(repo.gitDir, rebaseStateFile))
			})
		}
	}
}

func TestSubmoduleStack_PreservesLocalChanges(t *testing.T) {
	for _, command := range []string{"rebase", "sync"} {
		for _, change := range []string{"tracked", "untracked", "commit"} {
			t.Run(command+"/"+change, func(t *testing.T) {
				repo := setupSubmoduleStack(t, false, false, false)
				module := filepath.Join(repo.childDir, "module with spaces")
				switch change {
				case "tracked":
					issue250WriteFile(t, module, "module.txt", "local edits\n")
				case "untracked":
					issue250WriteFile(t, module, "untracked.txt", "local work\n")
				case "commit":
					issue250WriteFile(t, module, "module.txt", "local commit\n")
					issue250Git(t, module, "add", ".")
					issue250Git(t, module, "commit", "-m", "local submodule commit")
				}
				head := issue250Git(t, module, "rev-parse", "HEAD")
				diff := issue250Git(t, module, "diff")
				status := issue250Git(t, module, "status", "--porcelain")
				withIssue250Repo(t, repo.childDir)

				require.Error(t, runSubmoduleStackCommand(issue250TestConfig(t), command))

				assert.Equal(t, head, issue250Git(t, module, "rev-parse", "HEAD"))
				assert.Equal(t, diff, issue250Git(t, module, "diff"))
				assert.Equal(t, status, issue250Git(t, module, "status", "--porcelain"))
				for branch, sha := range repo.originalRefs {
					assert.Equal(t, sha, issue250Git(t, repo.dir, "rev-parse", branch))
				}
				assert.NoFileExists(t, filepath.Join(repo.gitDir, rebaseStateFile))
			})
		}
	}
}

func TestSubmoduleStack_Uninitialized(t *testing.T) {
	repo := setupSubmoduleStack(t, false, false, false)
	issue250Git(t, repo.dir, "submodule", "deinit", "module with spaces")
	withIssue250Repo(t, repo.dir)

	require.NoError(t, runSubmoduleStackCommand(issue250TestConfig(t), "rebase"))

	assert.NoFileExists(t, filepath.Join(repo.dir, "module with spaces", ".git"))
	assert.Empty(t, issue250Git(t, repo.dir, "status", "--porcelain"))
	require.NoError(t, issue250GitMayFail(t, repo.dir, "merge-base", "--is-ancestor", "main", "child"))
}

func runSubmoduleStackCommand(cfg *config.Config, command string) error {
	if command == "sync" {
		return runSync(cfg, &syncOptions{remote: "origin"})
	}
	return runRebase(cfg, &rebaseOptions{remote: "origin"})
}
