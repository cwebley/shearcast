package detect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWeights(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "w.json")
	os.WriteFile(path, []byte(`{"edge":"start","bias":-2.0,"weights":{"on_topic":-4.0,"anecdote":2.0}}`), 0o644)

	w, err := LoadWeights(path)
	if err != nil {
		t.Fatalf("LoadWeights: %v", err)
	}
	if w.Bias != -2.0 || w.Weights["on_topic"] != -4.0 {
		t.Errorf("got %+v", w)
	}
}

// A missing file means "no fitted model yet", which is a normal state, not a
// failure: the caller falls back to the single predicate.
func TestLoadWeightsMissingFileIsNotAnError(t *testing.T) {
	w, err := LoadWeights(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("got error %v, want nil", err)
	}
	if w != nil {
		t.Errorf("got %+v, want nil", w)
	}
}

func TestLoadWeightsRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "w.json")
	os.WriteFile(path, []byte(`{"edge":"start","bias":0}`), 0o644)
	if _, err := LoadWeights(path); err == nil {
		t.Error("expected an error for weights with no coefficients")
	}
}

func TestScoreSeparatesAdFromProgramme(t *testing.T) {
	w := &Weights{Bias: -2.0, Weights: map[string]float64{
		"on_topic": -4.0, "anecdote": 2.0, "benefit_claim": 2.5,
	}}
	programme := map[string]float64{"on_topic": 0.95, "anecdote": 0.10, "benefit_claim": 0.02}
	advert := map[string]float64{"on_topic": 0.10, "anecdote": 0.85, "benefit_claim": 0.80}

	lo, hi := w.Score(programme), w.Score(advert)
	if lo >= hi {
		t.Errorf("programme scored %.3f and advert %.3f; advert should score higher", lo, hi)
	}
	if lo > 0.2 {
		t.Errorf("programme scored %.3f, want it low", lo)
	}
	if hi < 0.5 {
		t.Errorf("advert scored %.3f, want it high", hi)
	}
}

// A coefficient whose feature was never measured must count as zero rather than
// silently skewing the score or panicking.
func TestScoreTreatsUnmeasuredFeaturesAsZero(t *testing.T) {
	w := &Weights{Bias: 0, Weights: map[string]float64{"present": 1.0, "absent": 9.0}}
	got := w.Score(map[string]float64{"present": 1.0})
	want := 1 / (1 + 1/2.718281828459045) // sigmoid(1.0)
	if got < want-1e-6 || got > want+1e-6 {
		t.Errorf("got %v, want sigmoid(1.0)=%v", got, want)
	}
	if missing := w.Missing(map[string]float64{"present": 1.0}); len(missing) != 1 || missing[0] != "absent" {
		t.Errorf("Missing: got %v, want [absent]", missing)
	}
}

func TestScoreIsBounded(t *testing.T) {
	w := &Weights{Bias: 1e6, Weights: map[string]float64{"x": 1e6}}
	if s := w.Score(map[string]float64{"x": 1e6}); s <= 0 || s > 1 {
		t.Errorf("got %v, want a probability", s)
	}
	w2 := &Weights{Bias: -1e6, Weights: map[string]float64{"x": -1e6}}
	if s := w2.Score(map[string]float64{"x": 1e6}); s < 0 || s >= 1 {
		t.Errorf("got %v, want a probability", s)
	}
}
