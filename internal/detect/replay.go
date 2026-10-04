package detect

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/transcript"
)

// EvidenceVersion identifies the recording format. Exact questions and states
// below identify the prompts; changing either requires newly collected answers.
const EvidenceVersion = 1

var ErrIncompleteReplay = errors.New("incomplete replay")

// Recording contains all inputs needed to rerun detection without a model or
// caption cache. Calls retain individual predicate repeats and feature answers.
// Only successful whole-episode recordings should be used for comparisons.
type Recording struct {
	Version  int              `json:"version"`
	Options  Options          `json:"options"`
	Cues     []transcript.Cue `json:"cues"`
	Duration float64          `json:"duration"`
	Calls    []EvidenceCall   `json:"calls"`
}

// EvidenceCall records one logical Ask or AskAll operation. Batches preserve the
// actual grouping used live, even though replay matches the collection of
// state/question pairs independently of batching and goroutine scheduling.
type EvidenceCall struct {
	Kind    string                `json:"kind"`
	Batches []jev.Batch           `json:"batches"`
	Answers map[string]jev.Answer `json:"answers"`
}

// RunRecorded runs the production detector and captures the evidence it uses.
// It neither changes prompts nor asks extra questions for unvisited branches.
func (d *Detector) RunRecorded(ctx context.Context, cues []transcript.Cue, duration float64) (*Result, *Recording, error) {
	r := &Recording{Version: EvidenceVersion, Options: d.Opts, Cues: cues, Duration: duration}
	c := &recordingClient{ModelClient: d.Client, recording: r}
	result, err := New(c, d.Opts).Run(ctx, cues, duration)
	if err != nil {
		return nil, nil, err
	}
	return result, r, nil
}

type recordingClient struct {
	ModelClient
	recording *Recording
}

func (c *recordingClient) Ask(ctx context.Context, state string, questions map[string]jev.Question) (*jev.Response, error) {
	response, err := c.ModelClient.Ask(ctx, state, questions)
	if err == nil {
		c.recording.Calls = append(c.recording.Calls, EvidenceCall{
			Kind: "ask", Batches: []jev.Batch{{State: state, Questions: questions}}, Answers: response.Answers,
		})
	}
	return response, err
}

func (c *recordingClient) AskAll(ctx context.Context, batches []jev.Batch, parallel int) (map[string]jev.Answer, error) {
	answers, err := c.ModelClient.AskAll(ctx, batches, parallel)
	if err == nil {
		c.recording.Calls = append(c.recording.Calls, EvidenceCall{Kind: "all", Batches: batches, Answers: answers})
	}
	return answers, err
}

// Replay executes the current production policy on recorded inputs. Passing nil
// options uses the recorded settings and weights. An unseen question, changed
// context, or exhausted repeat is an error, never an implicit zero or live call.
// Unused evidence is allowed when a candidate policy visits fewer branches.
func Replay(ctx context.Context, recording *Recording, options *Options) (*Result, error) {
	if recording == nil || recording.Version != EvidenceVersion {
		return nil, fmt.Errorf("%w: missing or unsupported recording version", ErrIncompleteReplay)
	}
	if len(recording.Cues) == 0 || recording.Duration <= 0 || math.IsInf(recording.Duration, 0) || math.IsNaN(recording.Duration) {
		return nil, fmt.Errorf("%w: missing captions or invalid duration", ErrIncompleteReplay)
	}
	c := &replayClient{calls: make(map[string][]EvidenceCall)}
	for _, call := range recording.Calls {
		key, err := evidenceKey(call.Kind, call.Batches)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrIncompleteReplay, err)
		}
		c.calls[key] = append(c.calls[key], call)
	}
	opts := recording.Options
	if options != nil {
		opts = *options
	}
	result, err := New(c, opts).Run(ctx, recording.Cues, recording.Duration)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIncompleteReplay, err)
	}
	return result, nil
}

type replayClient struct {
	calls map[string][]EvidenceCall
}

func (c *replayClient) Stats() jev.Stats { return jev.Stats{} }

func (c *replayClient) Ask(ctx context.Context, state string, questions map[string]jev.Question) (*jev.Response, error) {
	answers, err := c.take(ctx, "ask", []jev.Batch{{State: state, Questions: questions}})
	if err != nil {
		return nil, err
	}
	return &jev.Response{Answers: answers}, nil
}

func (c *replayClient) AskAll(ctx context.Context, batches []jev.Batch, _ int) (map[string]jev.Answer, error) {
	return c.take(ctx, "all", batches)
}

func (c *replayClient) take(ctx context.Context, kind string, batches []jev.Batch) (map[string]jev.Answer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := evidenceKey(kind, batches)
	if err != nil {
		return nil, err
	}
	calls := c.calls[key]
	if len(calls) == 0 {
		return nil, fmt.Errorf("no recorded answer for %s request %s (%d batches)", kind, key, len(batches))
	}
	c.calls[key] = calls[1:]
	return calls[0].Answers, nil
}

func evidenceKey(kind string, batches []jev.Batch) (string, error) {
	// ExtractFeatures currently chunks a map's keys in unspecified order. Match
	// the logical questions rather than that incidental partition. This does not
	// claim a new live batching scheme would produce the same model answers.
	var pairs []string
	for _, batch := range batches {
		for id, question := range batch.Questions {
			encoded, err := json.Marshal(struct {
				State    string
				ID       string
				Question jev.Question
			}{batch.State, id, question})
			if err != nil {
				return "", err
			}
			pairs = append(pairs, string(encoded))
		}
	}
	sort.Strings(pairs)
	data, err := json.Marshal(struct {
		Kind  string
		Pairs []string
	}{kind, pairs})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}
