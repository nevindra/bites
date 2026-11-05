package bites

import (
	"errors"
	"os"
)

// checkUploadDir reports why dir cannot hold an upload's temporary file: it is made and removed
// at once, so a typo or a read-only directory is a failure at start-up and not a 500 on the first
// large file (ADR 0044).
func checkUploadDir(dir string) error {
	st, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("it does not exist")
		}
		return err
	}
	if !st.IsDir() {
		return errors.New("it is not a directory")
	}
	f, err := os.CreateTemp(dir, "bites-check-*")
	if err != nil {
		return errors.New("it is not writable")
	}
	f.Close()
	os.Remove(f.Name())
	return nil
}

// uploadDirIsMemory reports whether the directory files are spilled to is on tmpfs or ramfs,
// where a file written is memory (ADR 0044).
func (a *App) uploadDirIsMemory() (string, bool) {
	dir := a.limits.UploadDir
	if dir == "" {
		dir = os.TempDir()
	}
	return dir, onTmpfs(dir)
}
