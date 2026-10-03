package main

import (
	"errors"
	"flag"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFlags(t *testing.T) {
	o, err := parseFlags([]string{"-rates", "100, 200", "-window", "2s", "-workload", "mixed"})
	require.NoError(t, err)
	assert.Equal(t, []float64{100, 200}, o.rates)
	assert.Equal(t, 2*time.Second, o.window)
	assert.Equal(t, "mixed", o.workload)
}

func TestParseFlags_Defaults(t *testing.T) {
	o, err := parseFlags(nil)
	require.NoError(t, err)
	assert.Equal(t, 4096, o.maxInFlight)
	assert.Equal(t, 5*time.Second, o.timeout)
	assert.Equal(t, 0.05, o.stopErrorRate)
	assert.Equal(t, []float64{250, 500, 1000, 2000}, o.rates)
}

func TestParseFlags_Accepts(t *testing.T) {
	for _, args := range [][]string{
		{"-warmup", "0"},
		{"-stop-error-rate", "0"},
		{"-stop-error-rate", "1"},
		{"-max-inflight", "1"},
		{"-url", "https://example.com:8443/"},
	} {
		_, err := parseFlags(args)
		assert.NoError(t, err, "%v", args)
	}
}

func TestParseFlags_RejectsNonsense(t *testing.T) {
	for name, args := range map[string][]string{
		"bad rate":            {"-rates", "100,x"},
		"zero rate":           {"-rates", "0"},
		"negative rate":       {"-rates", "-5"},
		"NaN rate":            {"-rates", "NaN"},
		"infinite rate":       {"-rates", "Inf"},
		"empty rate":          {"-rates", ""},
		"trailing comma":      {"-rates", "100,"},
		"descending rates":    {"-rates", "200,100"},
		"duplicate rates":     {"-rates", "100,100"},
		"unsorted rates":      {"-rates", "100,300,200"},
		"zero window":         {"-window", "0s"},
		"negative window":     {"-window", "-1s"},
		"negative warmup":     {"-warmup", "-1s"},
		"unknown workload":    {"-workload", "nope"},
		"zero max-inflight":   {"-max-inflight", "0"},
		"neg max-inflight":    {"-max-inflight", "-3"},
		"zero timeout":        {"-timeout", "0s"},
		"negative timeout":    {"-timeout", "-1s"},
		"error rate > 1":      {"-stop-error-rate", "1.01"},
		"error rate < 0":      {"-stop-error-rate", "-0.1"},
		"error rate NaN":      {"-stop-error-rate", "NaN"},
		"empty url":           {"-url", ""},
		"url without scheme":  {"-url", "127.0.0.1:3000"},
		"url wrong scheme":    {"-url", "ftp://host"},
		"url without host":    {"-url", "http://"},
		"unknown flag":        {"-bogus"},
		"positional argument": {"extra"},
	} {
		_, err := parseFlags(args)
		assert.Error(t, err, name)
	}
}

func TestParseFlags_ErrorsNameTheFlag(t *testing.T) {
	for flagName, args := range map[string][]string{
		"-max-inflight":    {"-max-inflight", "0"},
		"-timeout":         {"-timeout", "0s"},
		"-stop-error-rate": {"-stop-error-rate", "2"},
		"-warmup":          {"-warmup", "-1s"},
	} {
		_, err := parseFlags(args)
		require.Error(t, err)
		assert.Contains(t, err.Error(), flagName)
	}
}

func TestParseFlags_Help(t *testing.T) {
	_, err := parseFlags([]string{"-h"})
	assert.True(t, errors.Is(err, flag.ErrHelp))
}
