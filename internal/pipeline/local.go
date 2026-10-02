package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/fileutil"
	"github.com/cwebley/shearcast/internal/state"
)

// Local publication can become the retry artifact after audio and feed are
// durable. Save this checkpoint before removing the private managed copy.
// Caller-owned imports/custom renders remain caller-owned and stay in place.
func (r *Runner) adoptLocalPublication(ch config.Channel, id, audioKey string, ep *state.Episode) error {
	local, ok := r.Publisher.(interface{ LocalPath(string) (string, error) })
	if !ok {
		return nil
	}
	managed, err := fileutil.CanonicalPath(RenderPath(r.Cache, ch, id))
	if err != nil {
		return err
	}
	if ep.RenderPath != managed {
		return nil
	}
	path, err := local.LocalPath(audioKey)
	if err != nil {
		return err
	}
	hash, size, err := audioDigest(path)
	if err != nil {
		return err
	}
	if hash != ep.AudioSHA256 || size != ep.AudioBytes {
		return fmt.Errorf("published audio failed checksum verification")
	}
	ep.RecordPath, ep.RenderPath = managed+".json", path
	return nil
}

func (r *Runner) cleanupPublishedRender(ch config.Channel, id string, ep state.Episode) error {
	if ep.Stage != state.Published || ep.RecordPath == "" {
		return nil
	}
	managed, err := fileutil.CanonicalPath(RenderPath(r.Cache, ch, id))
	if err != nil {
		return err
	}
	if ep.RecordPath != managed+".json" || sameFile(managed, ep.RenderPath) {
		return nil
	}
	hash, size, err := audioDigest(managed)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if hash != ep.AudioSHA256 || size != ep.AudioBytes {
		return fmt.Errorf("managed render changed before publication cleanup")
	}
	// Verify the served copy again when recovering a crash after the state save.
	hash, size, err = audioDigest(ep.RenderPath)
	if err != nil {
		return err
	}
	if hash != ep.AudioSHA256 || size != ep.AudioBytes {
		return fmt.Errorf("published audio changed before render cleanup")
	}
	if err := os.Remove(managed); err != nil {
		return err
	}
	return fileutil.SyncDir(filepath.Dir(managed))
}
