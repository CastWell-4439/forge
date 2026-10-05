package worker

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// N2b decision 9: streaming is on unless explicitly off, and a typo leans
// toward "on" (streaming degrades to buffered responses on its own, so the
// benefit — no whole-response timeout — is what a silent disable would cost).
func TestLLMStreamEnabledDefaultsOn(t *testing.T) {
	t.Setenv(envLLMStream, "")
	assert.True(t, llmStreamEnabled(), "streaming is on by default")

	for _, on := range []string{"on", "1", "true", "yes", "TRUE", " On "} {
		t.Setenv(envLLMStream, on)
		assert.True(t, llmStreamEnabled(), "%q keeps streaming on", on)
	}

	for _, off := range []string{"off", "0", "false", "no", "OFF"} {
		t.Setenv(envLLMStream, off)
		assert.False(t, llmStreamEnabled(), "%q turns streaming off", off)
	}

	t.Setenv(envLLMStream, "ofl") // typo
	assert.True(t, llmStreamEnabled(), "a typo must not silently disable streaming")
}
