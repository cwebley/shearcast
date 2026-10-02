package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/subscribe"
)

func TestPublishSubscriptionsListsOnlyEnabledPublishedChannels(t *testing.T) {
	store := &memoryPublisher{objects: map[string][]byte{
		"live/feed.xml": []byte("<rss/>"),
		"off/feed.xml":  []byte("<rss/>"),
	}}
	channels := []config.Channel{
		{Slug: "live", Title: "Live show"},
		{Slug: "off", Title: "Disabled show", Disabled: true},
		{Slug: "new", Title: "Not yet published"},
	}
	url, err := PublishSubscriptions(context.Background(), store, channels)
	if err != nil {
		t.Fatal(err)
	}
	if url != store.PublicURL(subscribe.PageKey) {
		t.Errorf("url = %q", url)
	}
	for _, key := range []string{subscribe.PageKey, subscribe.OPMLKey} {
		body := string(store.objects[key])
		if !strings.Contains(body, "Live show") || !strings.Contains(body, store.PublicURL("live/feed.xml")) {
			t.Errorf("%s lacks the published channel:\n%s", key, body)
		}
		if strings.Contains(body, "Disabled show") || strings.Contains(body, "Not yet published") {
			t.Errorf("%s lists a disabled or unpublished channel:\n%s", key, body)
		}
	}
}
