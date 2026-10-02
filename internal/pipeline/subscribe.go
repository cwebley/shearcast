package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/storage"
	"github.com/cwebley/shearcast/internal/subscribe"
)

// PublishSubscriptions rewrites the subscription page and OPML list from every
// enabled channel whose feed exists, so a channel appears once it has
// published and disappears when disabled or purged. It returns the page URL.
func PublishSubscriptions(ctx context.Context, store Publisher, channels []config.Channel) (string, error) {
	var shows []subscribe.Show
	for _, ch := range channels {
		if ch.Disabled {
			continue
		}
		key := ch.Slug + "/feed.xml"
		if _, err := store.Get(ctx, key); errors.Is(err, storage.ErrNotFound) {
			continue
		} else if err != nil {
			return "", fmt.Errorf("checking %s: %w", key, err)
		}
		shows = append(shows, subscribe.Show{Title: ch.FeedTitle(), FeedURL: store.PublicURL(key)})
	}
	list, err := subscribe.OPML(shows)
	if err != nil {
		return "", err
	}
	page, err := subscribe.Page(shows)
	if err != nil {
		return "", err
	}
	// The list goes first so the page never links to a missing file.
	if _, err := store.Put(ctx, subscribe.OPMLKey, bytes.NewReader(list), int64(len(list)), subscribe.OPMLType); err != nil {
		return "", err
	}
	return store.Put(ctx, subscribe.PageKey, bytes.NewReader(page), int64(len(page)), subscribe.PageType)
}
