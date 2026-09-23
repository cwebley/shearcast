package jev

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"
)

// AskAll runs batches concurrently and merges their answers into one map.
//
// Question ids must be unique across batches, which they are by construction:
// each id encodes the window or chunk it came from. Jev answers every question
// in a batch against one shared encoding of the state, so the useful unit of
// parallelism is the batch, not the question.
func (c *Client) AskAll(ctx context.Context, batches []Batch, parallel int) (map[string]Answer, error) {
	if parallel < 1 {
		parallel = 1
	}
	answers := make([]map[string]Answer, len(batches))

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(parallel)
	for i, b := range batches {
		i, b := i, b
		g.Go(func() error {
			resp, err := c.Ask(ctx, b.State, b.Questions)
			if err != nil {
				return fmt.Errorf("batch %d: %w", i, err)
			}
			answers[i] = resp.Answers
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	merged := make(map[string]Answer)
	for _, set := range answers {
		for id, a := range set {
			merged[id] = a
		}
	}
	return merged, nil
}

// Chunk splits questions into batches of at most size, all sharing one state.
// Used when the state is small enough to repeat but the question count is not.
func Chunk(state string, questions map[string]Question, ids []string, size int) []Batch {
	if size < 1 {
		size = 1
	}
	var batches []Batch
	for start := 0; start < len(ids); start += size {
		end := min(start+size, len(ids))
		group := make(map[string]Question, end-start)
		for _, id := range ids[start:end] {
			if q, ok := questions[id]; ok {
				group[id] = q
			}
		}
		if len(group) > 0 {
			batches = append(batches, Batch{State: state, Questions: group})
		}
	}
	return batches
}
