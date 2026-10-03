package builder

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/SamyRai/cityFinder/lib/finder/coordinates"
	"github.com/SamyRai/cityFinder/lib/finder/name"
	"github.com/SamyRai/cityFinder/lib/finder/postalCode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWriteAll_RunsWritesConcurrently: two writes that each wait for the
// other can only finish if they run at the same time.
func TestWriteAll_RunsWritesConcurrently(t *testing.T) {
	aReady, bReady := make(chan struct{}), make(chan struct{})
	rendezvous := func(mine, theirs chan struct{}) Write {
		return func() error {
			close(mine)
			select {
			case <-theirs:
				return nil
			case <-time.After(5 * time.Second):
				return errors.New("the other write never started: writes are sequential")
			}
		}
	}
	require.NoError(t, WriteAll(context.Background(), rendezvous(aReady, bReady), nil, rendezvous(bReady, aReady)))
}

// TestWriteAll_ReturnsFirstErrorAfterAllFinish: a failing write does not
// abandon its siblings, and nothing is still running when WriteAll returns.
func TestWriteAll_ReturnsFirstErrorAfterAllFinish(t *testing.T) {
	base := runtime.NumGoroutine()
	boom := errors.New("boom")
	done := make(chan struct{})
	slow := func() error {
		time.Sleep(30 * time.Millisecond)
		close(done)
		return nil
	}
	err := WriteAll(context.Background(), slow, func() error { return boom })
	require.ErrorIs(t, err, boom)
	select {
	case <-done:
	default:
		t.Fatal("WriteAll returned before the slow write finished")
	}
	settleGoroutines(t, base)
}

// TestSourcesLoad_PostalFailureDrainsAndCitiesErrorWins: the two parses run
// concurrently; a failing one never leaves the other running, and when both
// fail the city error is the one reported, as when they ran in sequence.
func TestSourcesLoad_PostalFailureDrainsAndCitiesErrorWins(t *testing.T) {
	src := tinySources(t)
	require.NoError(t, os.Remove(src.PostalFile))

	base := runtime.NumGoroutine()
	_, _, err := src.Load(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Postal Code")
	settleGoroutines(t, base)

	require.NoError(t, os.Remove(src.CitiesFile))
	_, _, err = src.Load(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GeoNames data")
	settleGoroutines(t, base)
}

// TestSourcesLoad_CancelledContextStartsNothing: a cancelled boot does not parse
// multi-GB files.
func TestSourcesLoad_CancelledContextStartsNothing(t *testing.T) {
	src := tinySources(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := src.Load(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

// tinySources writes two cities and two postal codes under t.TempDir().
func tinySources(t *testing.T) Sources {
	t.Helper()
	dir := t.TempDir()
	cities := "2994701\tRoc Meler\tRoc Meler\tRoc Mele,Roc Meler\t42.58765\t1.7418\tT\tPK\tAD\tAD,FR\t02\t\t\t\t0\t2811\t2348\tEurope/Andorra\t2023-10-03\n" +
		"3040051\tles Escaldes\tles Escaldes\tEscaldes\t42.50729\t1.53414\tPPLA\tAD\tAD\t\t07\t\t\t\t16316\t\t\t1032\tEurope/Andorra\t2023-10-03\n"
	postal := "AD\tAD100\tCanillo\tCanillo\t02\t\t\t\t\t42.5833\t1.6667\t6\n" +
		"AD\tAD200\tEncamp\tEncamp\t03\t\t\t\t\t42.5347\t1.5801\t6\n"
	src := Sources{CitiesFile: filepath.Join(dir, "cities.txt"), PostalFile: filepath.Join(dir, "postal.txt")}
	require.NoError(t, os.WriteFile(src.CitiesFile, []byte(cities), 0o600))
	require.NoError(t, os.WriteFile(src.PostalFile, []byte(postal), 0o600))
	return src
}

// TestBuildAndWrite runs the whole sequence the way both callers do and
// checks the three files exist and decode, with the name index attached to
// the S2 city table.
func TestBuildAndWrite(t *testing.T) {
	src := tinySources(t)
	cities, postal, err := src.Load(context.Background())
	require.NoError(t, err)
	dir := t.TempDir()
	s2Path, namePath, postalPath := filepath.Join(dir, "s2.gob"), filepath.Join(dir, "name.gob"), filepath.Join(dir, "postal.gob")

	s2, writeS2, err := BuildS2(s2Path, cities)
	require.NoError(t, err)
	nameFinder, writeName, err := BuildName(namePath, cities, s2.Cities)
	require.NoError(t, err)
	assert.False(t, nameFinder.OwnsCityTable(), "the name index must share the S2 city table")
	_, writePostal, err := BuildPostal(postalPath, postal)
	require.NoError(t, err)
	require.NoError(t, WriteAll(context.Background(), writeS2, writeName, writePostal))

	gotS2, err := coordinates.DeserializeIndex(s2Path)
	require.NoError(t, err)
	gotName, err := name.DeserializeIndex(namePath)
	require.NoError(t, err)
	require.NoError(t, gotName.ShareCities(gotS2.Cities))
	_, err = postalCode.DeserializeIndex(postalPath)
	require.NoError(t, err)
}

// TestBuildName_MismatchedTableKeepsOwnCopy pins the single ShareCities
// policy: a table that cannot be shared is a warning, not a failure, and the
// index keeps its own copy.
func TestBuildName_MismatchedTableKeepsOwnCopy(t *testing.T) {
	cities, _, err := tinySources(t).Load(context.Background())
	require.NoError(t, err)
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	nameFinder, write, err := BuildName(filepath.Join(t.TempDir(), "name.gob"), cities, nil)
	require.NoError(t, err)
	assert.True(t, nameFinder.OwnsCityTable())
	assert.Contains(t, buf.String(), "keeps its own city table")
	require.NoError(t, write())
}

func TestWrites_ReportSerializeFailures(t *testing.T) {
	src := tinySources(t)
	cities, postal, err := src.Load(context.Background())
	require.NoError(t, err)
	bad := filepath.Join(t.TempDir(), "missing-dir", "x.gob")

	s2, writeS2, err := BuildS2(bad, cities)
	require.NoError(t, err)
	_, writeName, _ := BuildName(bad, cities, s2.Cities)
	_, writePostal, _ := BuildPostal(bad, postal)
	assert.ErrorContains(t, writeS2(), "failed to serialize S2 index")
	assert.ErrorContains(t, writeName(), "failed to serialize name index")
	assert.ErrorContains(t, writePostal(), "failed to serialize postal code index")
}

func TestSourcesLoad_ZeroCitiesIsErrNoCities(t *testing.T) {
	src := tinySources(t)
	require.NoError(t, os.WriteFile(src.CitiesFile, nil, 0o600))
	src.Options.ExcludeAdminDivisions = true

	_, _, err := src.Load(context.Background())
	require.ErrorIs(t, err, ErrNoCities)
	assert.Contains(t, err.Error(), src.CitiesFile)
	assert.Contains(t, err.Error(), "exclude_admin_divisions")

	_, err = src.LoadCities()
	require.ErrorIs(t, err, ErrNoCities)
}

func TestSourcesLoad_EmptyPostalIsAllowed(t *testing.T) {
	src := tinySources(t)
	require.NoError(t, os.WriteFile(src.PostalFile, nil, 0o600))

	cities, postal, err := src.Load(context.Background())
	require.NoError(t, err)
	assert.Len(t, cities, 2)
	assert.Empty(t, postal)
}
