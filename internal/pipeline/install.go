package pipeline

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cwebley/shearcast/internal/fileutil"
)

// The installation journal is durable before either final file is replaced.
// It carries the whole record so either side of the audio rename is recoverable.
type renderInstall struct {
	Staged string       `json:"staged"` // basename within the output directory
	Record RenderRecord `json:"record"`
}

func stagePrefix(path string) string {
	hash := sha256.Sum256([]byte(filepath.Base(path)))
	return fmt.Sprintf(".render-%x-", hash[:8])
}

func installRender(path, staged string, record RenderRecord) error {
	data, err := json.Marshal(renderInstall{Staged: filepath.Base(staged), Record: record})
	if err != nil {
		return err
	}
	if err := fileutil.WriteAtomic(path+".install.json", data, 0o600); err != nil {
		return err
	}
	return recoverInstall(path)
}

func recoverInstall(path string) error {
	data, err := os.ReadFile(path + ".install.json")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var install renderInstall
	if err := json.Unmarshal(data, &install); err != nil {
		return fmt.Errorf("reading render installation: %w", err)
	}
	if install.Staged != filepath.Base(install.Staged) || !strings.HasPrefix(install.Staged, stagePrefix(path)) || install.Record.AudioSHA256 == "" {
		return fmt.Errorf("invalid render installation record for %s", path)
	}
	staged := filepath.Join(filepath.Dir(path), install.Staged)
	candidate := staged
	if _, err := os.Stat(staged); os.IsNotExist(err) {
		candidate = path
	} else if err != nil {
		return err
	}
	hash, size, err := audioDigest(candidate)
	if err != nil {
		return err
	}
	if hash != install.Record.AudioSHA256 || size != install.Record.AudioBytes {
		return fmt.Errorf("incomplete render installation failed checksum verification")
	}
	if err := os.Remove(path + ".json"); err != nil && !os.IsNotExist(err) {
		return err
	}
	if candidate == staged {
		if err := os.Rename(staged, path); err != nil {
			return err
		}
	}
	if err := writeRenderRecord(path+".json", install.Record); err != nil {
		return err
	}
	if err := os.Remove(path + ".install.json"); err != nil {
		return err
	}
	return fileutil.SyncDir(filepath.Dir(path))
}

// Called only while holding the command lock, after recovery has completed.
func cleanupRenderStages(path string) error {
	entries, err := os.ReadDir(filepath.Dir(path))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), stagePrefix(path)) && !entry.IsDir() {
			if err := os.Remove(filepath.Join(filepath.Dir(path), entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
