package bandit

import (
	"math"
	"math/rand"
	"net/netip"
	"testing"
)

func closeFloat(a, b float64) bool {
	return math.Abs(a-b) <= 1e-10*math.Max(1, math.Abs(b))
}

// Compute the batch posterior from the sample mean and centered sum of squares,
// independently of the production sequential update.
func batchPosterior(values []float64) (mu, lambda, alpha, beta, variance float64) {
	n := float64(len(values))
	mean := 0.0
	for _, x := range values {
		mean += x
	}
	mean /= n
	ss := 0.0
	for _, x := range values {
		ss += (x - mean) * (x - mean)
	}
	lambda = 0.001 + n
	mu = n * mean / lambda
	alpha = 1 + n/2
	beta = 1 + ss/2 + 0.001*n*mean*mean/(2*lambda)
	if len(values) > 1 {
		variance = ss / (n - 1)
	}
	return
}

func TestNormalGammaMatchesClosedForm(t *testing.T) {
	for _, values := range [][]float64{{100}, {100, 200, 300}, {0, 0, 0}, {50, 50, 50, 50}, {0.5, 1.25, 3.75, 10.125}, {1000000, 1000001, 999999, 1000003}} {
		wantMu, wantLambda, wantAlpha, wantBeta, wantVariance := batchPosterior(values)
		for seed := int64(1); seed <= 12; seed++ {
			order := rand.New(rand.NewSource(seed)).Perm(len(values))
			node := NewArmNode(netip.MustParsePrefix("192.0.2.0/24"), nil)
			for _, i := range order {
				node.Update(true, values[i], 3000)
			}
			_, _, mu, lambda, alpha, beta := node.GetPosteriorParams()
			for _, pair := range [][2]float64{{mu, wantMu}, {lambda, wantLambda}, {alpha, wantAlpha}, {beta, wantBeta}, {node.Stats().VarLatency, wantVariance}} {
				if !closeFloat(pair[0], pair[1]) {
					t.Fatalf("samples=%v order=%v got %.12g want %.12g", values, order, pair[0], pair[1])
				}
			}
		}
	}
}

func TestFailuresDoNotChangeConditionalLatency(t *testing.T) {
	node := NewArmNode(netip.MustParsePrefix("192.0.2.0/24"), nil)
	for _, latency := range []float64{20, 30, 40} {
		node.Update(true, latency, 3000)
	}
	before := node.Stats()
	_, _, mu, lambda, alpha, beta := node.GetPosteriorParams()
	for i := 0; i < 5; i++ {
		node.Update(false, math.NaN(), 6000)
	}
	a, b, gotMu, gotLambda, gotAlpha, gotBeta := node.GetPosteriorParams()
	after := node.Stats()
	if a != 4 || b != 6 || after.Samples != 8 || after.Failures != 5 || after.Successes != 3 {
		t.Fatalf("lost Bernoulli observations: %+v alpha=%v beta=%v", after, a, b)
	}
	if mu != gotMu || lambda != gotLambda || alpha != gotAlpha || beta != gotBeta || before.VarLatency != after.VarLatency {
		t.Fatal("failed probes changed the successful-latency posterior")
	}
}
