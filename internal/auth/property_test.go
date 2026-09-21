package auth_test

import (
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/Quad4-Software/rss-discovery/internal/auth"
)

func TestPropertyHashDeterministic(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 50
	properties := gopter.NewProperties(parameters)
	properties.Property("hash stable", prop.ForAll(
		func(s string) bool {
			return auth.HashToken(s) == auth.HashToken(s)
		},
		gen.AnyString(),
	))
	properties.Property("rank monotonic", prop.ForAll(
		func(a, b int) bool {
			levels := []auth.Level{auth.LevelReadonly, auth.LevelStandard, auth.LevelPriority, auth.LevelAdmin}
			if a < 0 || b < 0 || a >= len(levels) || b >= len(levels) {
				return true
			}
			if a <= b {
				return levels[a].Rank() <= levels[b].Rank()
			}
			return levels[a].Rank() >= levels[b].Rank()
		},
		gen.IntRange(0, 3),
		gen.IntRange(0, 3),
	))
	properties.TestingRun(t)
}
