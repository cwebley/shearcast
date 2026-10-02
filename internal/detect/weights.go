package detect

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// Weights is a fitted linear combination of the per-sentence features.
//
// It is the entire learned artifact: a bias and one coefficient per feature.
// Fitting happens offline against labeled data, so nothing here depends on a
// live model or a scoring pipeline at serving time.
type Weights struct {
	Edge      string             `json:"edge"`
	Bias      float64            `json:"bias"`
	TrainedOn []string           `json:"trained_on"`
	Videos    int                `json:"videos"`
	Rows      int                `json:"rows"`
	Weights   map[string]float64 `json:"weights"`
}

// LoadWeights reads a fitted model. A missing file is not an error: it means
// fall back to the single predicate.
func LoadWeights(path string) (*Weights, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var w Weights
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if len(w.Weights) == 0 {
		return nil, fmt.Errorf("%s has no weights", path)
	}
	return &w, nil
}

// Score combines one sentence's feature values. A feature the extractor did not
// produce counts as zero rather than failing, so adding a coefficient for a
// feature that is not being measured degrades gracefully instead of panicking.
func (w *Weights) Score(values map[string]float64) float64 {
	z := w.Bias
	for id, coef := range w.Weights {
		z += coef * values[id]
	}
	return 1 / (1 + math.Exp(-clamp(z, -30, 30)))
}

// Missing lists coefficients that had no matching measured feature. Worth
// surfacing: it usually means the weights and the feature set drifted apart.
func (w *Weights) Missing(values map[string]float64) []string {
	var out []string
	for id := range w.Weights {
		if _, ok := values[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
