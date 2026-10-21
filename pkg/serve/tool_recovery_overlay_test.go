package serve

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecoveryProducerRecipeRemainsByteIdentical(t *testing.T) {
	require.Equal(t, "sha256:1477b169b90ad468977c826ea0a06505f1cb64308cb0e36ab488e6e6fb3f3b1d", digestOf([]byte(OverlayRecipe("/store", "/target"))), "Linux producer bytes must stay frozen on every host platform")
}
