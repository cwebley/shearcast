package pipeline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/cwebley/shearcast/internal/storage"
	"github.com/cwebley/shearcast/internal/subscribe"
)

// NewSyncPublisher shares feed snapshots for one invocation and avoids identical
// feed/subscription writes. Use a fresh instance for each sync. Failed mutations
// invalidate the snapshot: recovery must observe the remote outcome of a lost
// write response before adopting a publication or deleting media.
func NewSyncPublisher(p Publisher) Publisher {
	if _, ok := p.(interface{ syncSnapshot() }); ok {
		return p
	}
	cached := &syncPublisher{Publisher: p}
	if local, ok := p.(localPaths); ok {
		return &localSyncPublisher{syncPublisher: cached, local: local}
	}
	return cached
}

type syncPublisher struct {
	Publisher
	entries sync.Map // key -> *syncObject; independent channels never share a network lock
}

type syncObject struct {
	mu     sync.Mutex
	loaded bool
	data   []byte
	err    error
}

func (*syncPublisher) syncSnapshot() {}

func syncDocument(key string) bool {
	return strings.HasSuffix(key, "/feed.xml") || key == subscribe.PageKey || key == subscribe.OPMLKey
}

func (p *syncPublisher) object(key string) *syncObject {
	value, _ := p.entries.LoadOrStore(key, &syncObject{})
	return value.(*syncObject)
}

func (p *syncPublisher) load(ctx context.Context, key string, object *syncObject) ([]byte, error) {
	if !object.loaded {
		data, err := p.Publisher.Get(ctx, key)
		if err == nil || errors.Is(err, storage.ErrNotFound) {
			object.data, object.err, object.loaded = bytes.Clone(data), err, true
		}
		return data, err
	}
	return bytes.Clone(object.data), object.err
}

func (p *syncPublisher) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !syncDocument(key) {
		return p.Publisher.Get(ctx, key)
	}
	object := p.object(key)
	object.mu.Lock()
	defer object.mu.Unlock()
	return p.load(ctx, key, object)
}

func (p *syncPublisher) Put(ctx context.Context, key string, reader io.Reader, size int64, kind string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !syncDocument(key) {
		return p.Publisher.Put(ctx, key, reader, size, kind)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	object := p.object(key)
	object.mu.Lock()
	defer object.mu.Unlock()
	current, err := p.load(ctx, key, object)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return "", err
	}
	if err == nil && bytes.Equal(current, data) {
		return p.PublicURL(key), nil
	}
	url, err := p.Publisher.Put(ctx, key, bytes.NewReader(data), size, kind)
	if err != nil {
		object.loaded = false
		return "", err
	}
	object.data, object.err, object.loaded = bytes.Clone(data), nil, true
	return url, nil
}

func (p *syncPublisher) Delete(ctx context.Context, key string) error {
	if !syncDocument(key) {
		return p.Publisher.Delete(ctx, key)
	}
	object := p.object(key)
	object.mu.Lock()
	defer object.mu.Unlock()
	err := p.Publisher.Delete(ctx, key)
	object.loaded = false
	if err == nil {
		object.data, object.err, object.loaded = nil, storage.ErrNotFound, true
	}
	return err
}

type localPaths interface{ LocalPath(string) (string, error) }
type localSyncPublisher struct {
	*syncPublisher
	local localPaths
}

func (p *localSyncPublisher) LocalPath(key string) (string, error) { return p.local.LocalPath(key) }
