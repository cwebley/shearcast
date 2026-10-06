package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"golang.org/x/sync/errgroup"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/storage"
	"github.com/cwebley/shearcast/internal/subscribe"
)

// PublishSubscriptions rewrites the subscription page and OPML list from every
// enabled channel whose feed exists, so a channel appears once it has
// published and disappears when disabled or purged. It returns the page URL.
func PublishSubscriptions(ctx context.Context, store Publisher, channels []config.Channel) (string, error) {
	present := make([]bool, len(channels))
	g, checkCtx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	for i, ch := range channels {
		if ch.Disabled {
			continue
		}
		g.Go(func() error {
			key := ch.Slug + "/feed.xml"
			if _, err := store.Get(checkCtx, key); errors.Is(err, storage.ErrNotFound) {
				return nil
			} else if err != nil {
				return fmt.Errorf("checking %s: %w", key, err)
			}
			present[i] = true
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return "", err
	}
	var shows []subscribe.Show
	for i, ch := range channels {
		if present[i] {
			shows = append(shows, subscribe.Show{Title: ch.FeedTitle(), FeedURL: store.PublicURL(ch.Slug + "/feed.xml")})
		}
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
