package finder

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFinderFacadeNilSubFinders pins that every facade method degrades on a
// Finder missing its sub-finder instead of panicking.
func TestFinderFacadeNilSubFinders(t *testing.T) {
	var bare Finder

	assert.Nil(t, bare.FindCityByName("Paris", "FR"))
	assert.Nil(t, bare.FindCityByPostalCode("75001", "FR"))
	assert.Nil(t, bare.PrefixNames("FR", "Pa", 10))
	assert.NotPanics(t, bare.WarmFuzzy)
	assert.NoError(t, bare.WaitFuzzy(context.Background()))
	assert.EqualValues(t, 0, bare.FuzzyBuildState())
	assert.EqualValues(t, 0, bare.FuzzyBudgetTrips())

	c, dist, err := bare.FindNearestCity(48.85, 2.35, 0)
	assert.Nil(t, c)
	assert.Zero(t, dist)
	assert.ErrorIs(t, err, ErrNoCoordinateIndex)
}
