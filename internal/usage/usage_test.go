package usage

import "testing"

func TestIntervalDropsEarlierPricingButKeepsObservedFreeUsage(t *testing.T) {
	first := Summary{Attempts: 1}
	i, o, cost := 100, 10, 0.0
	first.Observe(&i, &o, &cost, ModelRate(JevModel))
	failed := first.Add(Summary{Attempts: 1, MissingUsage: 1, MissingCost: 1})
	interval := failed.Since(first)
	if len(interval.Calculations) != 0 || interval.Cost != 0 {
		t.Fatalf("failed interval inherited prior pricing: %+v", interval)
	}
	free := Summary{Attempts: 1}
	zero := 0
	free.Observe(&zero, &zero, &cost, ModelRate(JevModel))
	interval = first.Add(free).Since(first)
	if len(interval.Calculations) != 1 || interval.Cost != 0 || interval.Attempts != 1 {
		t.Fatalf("lost observed zero-token calculation: %+v", interval)
	}
	freeRate := &Rate{Model: "test/free", Source: "test"}
	free = Summary{Attempts: 1}
	free.Observe(&i, &o, &cost, freeRate)
	interval = first.Add(free).Since(first)
	if len(interval.Calculations) != 1 || interval.Calculations[0].Rate.Model != "test/free" || interval.InputTokens != 100 {
		t.Fatalf("bad free-model interval: %+v", interval)
	}
}
