package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The shell owns dispatch and exit codes; the roles themselves are the
// shared Run functions (their behaviour is covered by the moved serve
// packages' tests).
func TestRunHelpExitsZero(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"help"}, &out, &errOut)

	assert.Equal(t, 0, code)
	assert.Contains(t, out.String(), "forge standalone")
	assert.Contains(t, out.String(), "forge coordinator")
	assert.Contains(t, out.String(), "forge worker")
	assert.Empty(t, errOut.String())
}

func TestRunNoArgsPrintsUsageAndFails(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(nil, &out, &errOut)

	assert.Equal(t, 1, code, "no subcommand is a usage error, not a silent default")
	assert.Contains(t, errOut.String(), "Usage:")
	assert.Empty(t, out.String())
}

func TestRunUnknownCommandFails(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"frobnicate"}, &out, &errOut)

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut.String(), `unknown command "frobnicate"`)
	assert.Contains(t, errOut.String(), "Usage:")
}

// Help aliases behave like help, not like unknown commands.
func TestRunHelpFlags(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		var out, errOut bytes.Buffer
		assert.Equal(t, 0, run([]string{flag}, &out, &errOut), flag)
		assert.Contains(t, out.String(), "Usage:")
	}
}
