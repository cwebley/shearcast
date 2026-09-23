package detect

// Feature extraction.
//
// The single predicate asks one hard question and makes the model do the
// combining internally, which produced a curve that ramped 0.27 to 0.83 with no
// step at the true boundary. This asks narrow factual questions instead and
// does the combining in code, where it can be fitted, printed and argued with.
//
// The split is deliberate. Narrow classification is what this model is good at;
// leaping from facts to a judgment is what it is bad at. Decomposition is known
// to win where a single question fails and to lose where one already works.

import (
	"context"
	"fmt"
	"strings"

	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/transcript"
)

// Feature is one narrow question asked of every candidate sentence. prev is the
// sentence immediately before it, which is empty for the first candidate.
type Feature struct {
	ID  string
	Ask func(s, prev transcript.Sentence) string
}

// Features is the extraction set used to score the start edge when fitted
// weights are configured (see Options.StartWeights).
func Features(subject string) []Feature {
	about := subject
	if about == "" {
		about = "the programme's own stated subject"
	}
	// solo asks about one sentence in isolation.
	solo := func(id, text string) Feature {
		return Feature{ID: id, Ask: func(s, _ transcript.Sentence) string {
			return fmt.Sprintf("Sentence [%s] is: %q\n\n%s", s.ID, s.Text, text)
		}}
	}
	// seam asks about the join between a sentence and the one before it. A
	// boundary is a discontinuity, and every other feature describes a sentence
	// in isolation, so this is the only one looking at the thing we want.
	seam := func(id, text string) Feature {
		return Feature{ID: id, Ask: func(s, prev transcript.Sentence) string {
			if prev.Text == "" {
				return fmt.Sprintf("Sentence [%s] is: %q\n\nThere is no preceding sentence. %s", s.ID, s.Text, text)
			}
			return fmt.Sprintf("The preceding sentence is: %q\nSentence [%s] is: %q\n\n%s",
				prev.Text, s.ID, s.Text, text)
		}}
	}

	return []Feature{
		solo("first_person", "The speaker is recounting their own personal experience or habits."),
		solo("names_brand", "This sentence names a specific company, product, service or app."),
		solo("addresses_listener", "This sentence speaks directly to the listener about something they might do."),
		solo("on_topic", fmt.Sprintf("This sentence is about %s.", about)),
		solo("benefit_claim", "This sentence describes what a product or service can do, or how well it does it."),
		solo("anecdote", "This sentence is part of a self-contained story or worked example, rather than direct exposition of the programme's subject."),
		solo("dated_fact", "This sentence states a specific year, date or dated event."),
		solo("transition", "This sentence is a pivot that moves from one subject to a different one."),
		solo("gratitude", "This sentence thanks, credits or acknowledges a supporter, sponsor or backer."),
		solo("could_be_removed", "If this sentence were deleted, the programme would lose nothing of its own argument."),
		solo("problem_setup", "This sentence describes a problem, frustration, difficulty or inconvenience."),
		solo("product_category", "This sentence refers to a category of commercial product or service, such as software, bedding, meal kits, insurance or mobile data."),
		seam("continues_previous", "Sentence [%s] continues the same thought as the sentence before it, rather than starting a new one."),
		solo("production_meta", "This sentence describes the people, effort or process behind making the programme itself, such as its writers, editors, researchers or production team, rather than the programme's own subject matter."),
	}
}

// lexical features cost no API call at all. Their errors are orthogonal to a
// model's, so stacking the two is worth more than either alone.
var lexicalSets = map[string][]string{
	"lex_thanks": {"thanks to", "thank you to", "sponsored by", "supported by", "brought to you by"},
	"lex_cta":    {"head to", "check out", "sign up", "use code", "go to ", "visit ", "download the", "try it"},
	"lex_url":    {".com", ".co/", ".io", "http", "slash "},
}

// Lexical computes the no-API features for one sentence.
func Lexical(s transcript.Sentence) map[string]float64 {
	text := strings.ToLower(s.Text)
	out := make(map[string]float64, len(lexicalSets))
	for id, needles := range lexicalSets {
		out[id] = 0
		for _, n := range needles {
			if strings.Contains(text, n) {
				out[id] = 1
				break
			}
		}
	}
	return out
}

// LexicalIDs lists the no-API feature names.
func LexicalIDs() []string {
	ids := make([]string, 0, len(lexicalSets))
	for id := range lexicalSets {
		ids = append(ids, id)
	}
	return ids
}

// ExtractFeatures asks every feature of every candidate in one request.
func (d *Detector) ExtractFeatures(ctx context.Context, cands []transcript.Sentence, state string, feats []Feature) (map[string]map[string]float64, error) {
	questions := make(map[string]jev.Question, len(cands)*len(feats))
	for i, s := range cands {
		var prev transcript.Sentence
		if i > 0 {
			prev = cands[i-1]
		}
		for _, f := range feats {
			questions[f.ID+"|"+s.ID] = jev.Noul(f.Ask(s, prev))
		}
	}

	ids := make([]string, 0, len(questions))
	for id := range questions {
		ids = append(ids, id)
	}
	answers, err := d.Client.AskAll(ctx, jev.Chunk(state, questions, ids, 400), d.Opts.Parallel)
	if err != nil {
		return nil, err
	}

	out := make(map[string]map[string]float64, len(cands))
	for _, s := range cands {
		row := make(map[string]float64, len(feats))
		for _, f := range feats {
			row[f.ID] = answers[f.ID+"|"+s.ID].Noul
		}
		for id, v := range Lexical(s) {
			row[id] = v
		}
		out[s.ID] = row
	}
	return out, nil
}
