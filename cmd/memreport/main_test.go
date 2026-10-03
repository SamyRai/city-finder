package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGenFlags(t *testing.T) {
	o, err := parseGenFlags([]string{"-dir", "d", "-n", "10", "-seed", "5"}, &bytes.Buffer{})
	require.NoError(t, err)
	assert.Equal(t, genOptions{dir: "d", n: 10, seed: 5}, o)

	o, err = parseGenFlags([]string{"-dir", "d"}, &bytes.Buffer{})
	require.NoError(t, err)
	assert.Equal(t, 2_000_000, o.n)
	assert.EqualValues(t, 1, o.seed)
}

func TestParseGenFlags_Rejects(t *testing.T) {
	for name, args := range map[string][]string{
		"missing dir": {},
		"empty dir":   {"-dir", ""},
		"zero n":      {"-dir", "d", "-n", "0"},
		"negative n":  {"-dir", "d", "-n", "-4"},
		"bad n":       {"-dir", "d", "-n", "x"},
		"extra arg":   {"-dir", "d", "extra"},
		"unknown":     {"-bogus"},
	} {
		_, err := parseGenFlags(args, &bytes.Buffer{})
		assert.Error(t, err, name)
	}
}

func TestParseMeasureFlags(t *testing.T) {
	o, err := parseMeasureFlags([]string{"-config", "c.json", "-dump", "a", "-profile", "p", "-fuzzy-timeout", "3s"}, &bytes.Buffer{})
	require.NoError(t, err)
	assert.Equal(t, measureOptions{cfgPath: "c.json", dumpPath: "a", profilePath: "p", fuzzyTimeout: 3 * time.Second}, o)

	o, err = parseMeasureFlags([]string{"-config", "c.json"}, &bytes.Buffer{})
	require.NoError(t, err)
	assert.Equal(t, defaultFuzzyTimeout, o.fuzzyTimeout)
}

func TestParseMeasureFlags_Rejects(t *testing.T) {
	for name, args := range map[string][]string{
		"missing config":   {},
		"zero timeout":     {"-config", "c", "-fuzzy-timeout", "0s"},
		"negative timeout": {"-config", "c", "-fuzzy-timeout", "-1m"},
		"extra arg":        {"-config", "c", "extra"},
		"unknown":          {"-bogus"},
	} {
		_, err := parseMeasureFlags(args, &bytes.Buffer{})
		assert.Error(t, err, name)
	}
}

func TestRun_Dispatch(t *testing.T) {
	ctx := context.Background()
	var out, errOut bytes.Buffer

	assert.ErrorIs(t, run(ctx, nil, &out, &errOut), errUsage)
	assert.ErrorIs(t, run(ctx, []string{"nope"}, &out, &errOut), errUsage)
	assert.ErrorIs(t, run(ctx, []string{"gen"}, &out, &errOut), errUsage)
	assert.ErrorIs(t, run(ctx, []string{"measure"}, &out, &errOut), errUsage)

	err := run(ctx, []string{"gen", "-h"}, &out, &errOut)
	assert.True(t, errors.Is(err, flag.ErrHelp))
	assert.Contains(t, errOut.String(), "-dir")
}

func TestRun_GenThenMeasureMissingConfigFails(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, run(context.Background(), []string{"gen", "-dir", dir, "-n", "50"}, &bytes.Buffer{}, &bytes.Buffer{}))
	_, err := os.Stat(filepath.Join(dir, "config.json"))
	require.NoError(t, err)

	err = run(context.Background(), []string{"measure", "-config", filepath.Join(dir, "absent.json")}, &bytes.Buffer{}, &bytes.Buffer{})
	require.Error(t, err)
	assert.NotErrorIs(t, err, errUsage, "a failed run is not a usage error")
}
