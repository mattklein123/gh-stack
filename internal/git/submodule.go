package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (d *defaultOps) submodulePins() (map[string]string, error) {
	out, err := d.runRaw("ls-files", "--stage", "--full-name", "-z", "--", ":/")
	if err != nil {
		return nil, fmt.Errorf("reading submodule pins: %w", err)
	}
	pins := make(map[string]string)
	for entry := range strings.SplitSeq(out, "\x00") {
		header, path, ok := strings.Cut(entry, "\t")
		if !ok || !strings.HasPrefix(header, "160000 ") || !strings.HasSuffix(header, " 0") {
			continue
		}
		pins[path] = strings.TrimSuffix(strings.TrimPrefix(header, "160000 "), " 0")
	}
	return pins, nil
}

func (d *defaultOps) submoduleMatches(root, path, pin string) (bool, error) {
	dir := filepath.Join(root, path)
	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	head, err := d.run("-C", dir, "rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	if head != pin {
		return false, nil
	}
	status, err := d.runRaw("-C", dir, "status", "--porcelain", "--untracked-files=all", "--ignore-submodules=none")
	return status == "", err
}

// Only move initialized submodules that were clean and at their recorded pin.
// In particular, do not hide gitlink changes from rebase's patch comparison.
func (d *defaultOps) withSubmoduleUpdate(operation func() error) error {
	pins, err := d.submodulePins()
	if err != nil {
		return err
	}
	if len(pins) == 0 {
		return operation()
	}
	root, err := d.RootDir()
	if err != nil {
		return err
	}
	for path, pin := range pins {
		clean, err := d.submoduleMatches(root, path, pin)
		if err != nil {
			return fmt.Errorf("checking submodule %q: %w", path, err)
		}
		if !clean {
			delete(pins, path)
		}
	}
	operationErr := operation()
	if len(pins) == 0 {
		return operationErr
	}
	updated, err := d.submodulePins()
	if err != nil {
		return errors.Join(operationErr, err)
	}
	for path, pin := range pins {
		if updated[path] == "" || updated[path] == pin {
			continue
		}
		clean, err := d.submoduleMatches(root, path, pin)
		if err != nil {
			return errors.Join(operationErr, fmt.Errorf("checking submodule %q after Git operation: %w", path, err))
		}
		if !clean {
			return errors.Join(operationErr, fmt.Errorf("submodule %q changed during the Git operation; leaving it untouched", path))
		}
		if err := d.runSilent("-C", root, "submodule", "update", "--checkout", "--recursive", "--", ":(literal)"+path); err != nil {
			return errors.Join(operationErr, fmt.Errorf("updating submodule %q: %w", path, err))
		}
	}
	return operationErr
}

func (d *defaultOps) runWithSubmoduleUpdate(args ...string) error {
	return d.withSubmoduleUpdate(func() error {
		return d.runSilent(append([]string{"-c", "submodule.recurse=false"}, args...)...)
	})
}
