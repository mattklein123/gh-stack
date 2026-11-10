package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupSubmoduleRepo(t *testing.T, nested bool) (*defaultOps, string, string, string, string) {
	t.Helper()
	root, dir := setupWorktreeRepo(t)
	source := setupLocalRepo(t)
	var nestedSource string
	if nested {
		nestedSource = setupLocalRepo(t)
		gitExec(t, source, "-c", "protocol.file.allow=always", "submodule", "add", nestedSource, "nested module")
		gitExec(t, source, "commit", "-am", "add nested submodule")
	}
	oldPin := gitExec(t, source, "rev-parse", "HEAD")
	writeFile(t, source, "init.txt", "updated")
	if nested {
		writeFile(t, nestedSource, "init.txt", "nested update")
		gitExec(t, nestedSource, "commit", "-am", "nested update")
		gitExec(t, filepath.Join(source, "nested module"), "fetch", "origin")
		gitExec(t, filepath.Join(source, "nested module"), "checkout", "origin/main")
	}
	gitExec(t, source, "add", ".")
	gitExec(t, source, "commit", "-m", "update module")
	newPin := gitExec(t, source, "rev-parse", "HEAD")
	gitExec(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", source, "module with spaces")
	module := filepath.Join(dir, "module with spaces")
	gitExec(t, module, "checkout", oldPin)
	gitExec(t, dir, "commit", "-am", "add submodule")
	gitExec(t, dir, "branch", "feature")
	gitExec(t, module, "checkout", newPin)
	gitExec(t, dir, "commit", "-am", "update submodule")
	gitExec(t, dir, "checkout", "feature")
	gitExec(t, module, "checkout", oldPin)
	if nested {
		gitExec(t, dir, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive")
	}
	return root, dir, module, oldPin, newPin
}

func TestIntegration_SubmoduleTransitions(t *testing.T) {
	for _, operation := range []string{"checkout", "merge", "reset", "rebase", "onto"} {
		t.Run(operation, func(t *testing.T) {
			root, dir, module, _, newPin := setupSubmoduleRepo(t, false)
			gitExec(t, dir, "config", "submodule.recurse", "true")
			gitExec(t, dir, "config", "submodule.module with spaces.update", "none")
			// Invoke from below the root to exercise repository-relative paths.
			nested := filepath.Join(dir, "directory")
			require.NoError(t, os.Mkdir(nested, 0755))
			target := requireWorktree(t, root, nested)
			var err error
			switch operation {
			case "checkout":
				err = target.CheckoutBranch("main")
			case "merge":
				err = target.MergeFF("main")
			case "reset":
				err = target.ResetHard("main")
			case "rebase":
				err = target.Rebase("main", RebaseOpts{})
			case "onto":
				oldBase := gitExec(t, dir, "rev-parse", "feature")
				err = target.RebaseOnto("main", oldBase, "feature", RebaseOpts{})
			}
			require.NoError(t, err)
			assert.Equal(t, newPin, gitExec(t, module, "rev-parse", "HEAD"))
			assert.Empty(t, gitExec(t, dir, "status", "--porcelain", "--ignore-submodules=none"))
			assert.Equal(t, "true", gitExec(t, dir, "config", "submodule.recurse"))
			assert.Equal(t, "none", gitExec(t, dir, "config", "submodule.module with spaces.update"))
		})
	}
}

func TestIntegration_SubmoduleNested(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		name := "clean"
		if dirty {
			name = "dirty"
		}
		t.Run(name, func(t *testing.T) {
			root, dir, module, oldPin, newPin := setupSubmoduleRepo(t, true)
			nested := filepath.Join(module, "nested module")
			before := gitExec(t, nested, "rev-parse", "HEAD")
			if dirty {
				writeFile(t, nested, "local.txt", "nested work")
			}

			require.NoError(t, root.CheckoutBranch("main"))

			if dirty {
				assert.Equal(t, oldPin, gitExec(t, module, "rev-parse", "HEAD"))
				assert.Equal(t, before, gitExec(t, nested, "rev-parse", "HEAD"))
				data, err := os.ReadFile(filepath.Join(nested, "local.txt"))
				require.NoError(t, err)
				assert.Equal(t, "nested work", string(data))
			} else {
				assert.Equal(t, newPin, gitExec(t, module, "rev-parse", "HEAD"))
				assert.NotEqual(t, before, gitExec(t, nested, "rev-parse", "HEAD"))
				assert.Empty(t, gitExec(t, dir, "status", "--porcelain", "--ignore-submodules=none"))
			}
		})
	}
}

func TestIntegration_SubmodulePreservesPreexistingChanges(t *testing.T) {
	for _, change := range []string{"tracked", "untracked", "commit"} {
		t.Run(change, func(t *testing.T) {
			root, dir, module, oldPin, _ := setupSubmoduleRepo(t, false)
			// Safety must not depend on the user's submodule ignore setting.
			gitExec(t, dir, "config", "submodule.module with spaces.ignore", "all")
			switch change {
			case "tracked":
				writeFile(t, module, "init.txt", "local edits")
			case "untracked":
				writeFile(t, module, "local.txt", "local work")
			case "commit":
				writeFile(t, module, "init.txt", "local commit")
				gitExec(t, module, "commit", "-am", "local commit")
				oldPin = gitExec(t, module, "rev-parse", "HEAD")
			}
			status := gitExec(t, module, "status", "--porcelain")
			diff := gitExec(t, module, "diff")

			require.NoError(t, root.CheckoutBranch("main"))

			assert.Equal(t, oldPin, gitExec(t, module, "rev-parse", "HEAD"))
			assert.Equal(t, status, gitExec(t, module, "status", "--porcelain"))
			assert.Equal(t, diff, gitExec(t, module, "diff"))
		})
	}
}

func TestIntegration_SubmoduleOperationFailure(t *testing.T) {
	root, dir, module, _, newPin := setupSubmoduleRepo(t, false)
	operationErr := errors.New("operation failed after updating the index")

	err := root.withSubmoduleUpdate(func() error {
		require.NoError(t, root.runSilent("checkout", "main"))
		return operationErr
	})

	require.ErrorIs(t, err, operationErr)
	assert.Equal(t, newPin, gitExec(t, module, "rev-parse", "HEAD"))
	assert.Empty(t, gitExec(t, dir, "status", "--porcelain"))
}

func TestIntegration_SubmoduleChangedDuringOperation(t *testing.T) {
	root, _, module, oldPin, _ := setupSubmoduleRepo(t, false)

	err := root.withSubmoduleUpdate(func() error {
		require.NoError(t, root.runSilent("checkout", "main"))
		writeFile(t, module, "init.txt", "concurrent work")
		return nil
	})

	require.ErrorContains(t, err, "changed during the Git operation")
	assert.Equal(t, oldPin, gitExec(t, module, "rev-parse", "HEAD"))
	data, readErr := os.ReadFile(filepath.Join(module, "init.txt"))
	require.NoError(t, readErr)
	assert.Equal(t, "concurrent work", string(data))
}

func TestIntegration_SubmoduleUpdateFailure(t *testing.T) {
	root, dir, module, oldPin, _ := setupSubmoduleRepo(t, false)
	missing := strings.Repeat("a", 40)
	operationErr := errors.New("original operation failure")

	err := root.withSubmoduleUpdate(func() error {
		require.NoError(t, root.runSilent("update-index", "--cacheinfo", "160000", missing, "module with spaces"))
		return operationErr
	})

	require.ErrorIs(t, err, operationErr)
	require.ErrorContains(t, err, `updating submodule "module with spaces"`)
	assert.Equal(t, oldPin, gitExec(t, module, "rev-parse", "HEAD"))
	assert.Equal(t, missing, gitExec(t, dir, "rev-parse", ":module with spaces"))
}
