package postalCode

import (
	"os"
	"testing"

	"github.com/SamyRai/cityFinder/lib/dataLoader"
	"github.com/stretchr/testify/assert"
)

// Test data for postal code tests
var testPostalCodeEntries = []dataLoader.PostalCodeEntry{
	{
		CountryCode: "US",
		PostalCode:  "10001",
		PlaceName:   "New York",
		Latitude:    40.7505,
		Longitude:   -73.9934,
		Accuracy:    1,
	},
	{
		CountryCode: "US",
		PostalCode:  "90210",
		PlaceName:   "Beverly Hills",
		Latitude:    34.0901,
		Longitude:   -118.4065,
		Accuracy:    1,
	},
	{
		CountryCode: "CA",
		PostalCode:  "M5V",
		PlaceName:   "Toronto",
		Latitude:    43.6532,
		Longitude:   -79.3832,
		Accuracy:    1,
	},
	{
		CountryCode: "GB",
		PostalCode:  "SW1A",
		PlaceName:   "London",
		Latitude:    51.5074,
		Longitude:   -0.1278,
		Accuracy:    1,
	},
}

func TestNewPostalCodeFinder(t *testing.T) {
	finder := NewPostalCodeFinder()
	assert.NotNil(t, finder)
	assert.Zero(t, finder.Len())
}

func TestAddPostalCode(t *testing.T) {
	finder := NewPostalCodeFinder()

	// Add a single entry
	entry := testPostalCodeEntries[0]
	finder.AddPostalCode(entry)

	// Verify it's stored correctly
	city := finder.CityByPostalCode("10001", "US")
	assert.NotNil(t, city)
	assert.Equal(t, "New York", city.Name)
	assert.Equal(t, "US", city.Country)
	assert.InDelta(t, 40.7505, city.Latitude, 0.0001)
	assert.InDelta(t, -73.9934, city.Longitude, 0.0001)
}

func TestAddPostalCode_OverwriteExisting(t *testing.T) {
	finder := NewPostalCodeFinder()

	// Add original entry
	originalEntry := testPostalCodeEntries[0]
	finder.AddPostalCode(originalEntry)

	// Add updated entry with same postal code
	updatedEntry := dataLoader.PostalCodeEntry{
		CountryCode: "US",
		PostalCode:  "10001",
		PlaceName:   "Updated New York",
		Latitude:    40.7506,
		Longitude:   -73.9935,
		Accuracy:    1,
	}
	finder.AddPostalCode(updatedEntry)

	// Verify it's updated
	city := finder.CityByPostalCode("10001", "US")
	assert.NotNil(t, city)
	assert.Equal(t, "Updated New York", city.Name)
	assert.InDelta(t, 40.7506, city.Latitude, 0.0001)
	assert.InDelta(t, -73.9935, city.Longitude, 0.0001)
}

func TestBuildIndex(t *testing.T) {
	// Create test data map
	testData := make(map[string]map[string]dataLoader.PostalCodeEntry)

	// Add test entries
	for _, entry := range testPostalCodeEntries {
		if testData[entry.CountryCode] == nil {
			testData[entry.CountryCode] = make(map[string]dataLoader.PostalCodeEntry)
		}
		testData[entry.CountryCode][entry.PostalCode] = entry
	}

	finder := BuildIndex(testData)
	assert.NotNil(t, finder)

	// Test lookups
	city := finder.CityByPostalCode("10001", "US")
	assert.NotNil(t, city)
	assert.Equal(t, "New York", city.Name)

	city = finder.CityByPostalCode("90210", "US")
	assert.NotNil(t, city)
	assert.Equal(t, "Beverly Hills", city.Name)

	city = finder.CityByPostalCode("M5V", "CA")
	assert.NotNil(t, city)
	assert.Equal(t, "Toronto", city.Name)

	city = finder.CityByPostalCode("SW1A", "GB")
	assert.NotNil(t, city)
	assert.Equal(t, "London", city.Name)
}

func TestCityByPostalCode(t *testing.T) {
	finder := NewPostalCodeFinder()

	// Add test entries
	for _, entry := range testPostalCodeEntries {
		finder.AddPostalCode(entry)
	}

	tests := []struct {
		name         string
		postalCode   string
		countryCode  string
		expectedName string
		expectNil    bool
	}{
		{"Valid US postal code", "10001", "US", "New York", false},
		{"Valid CA postal code", "M5V", "CA", "Toronto", false},
		{"Valid GB postal code", "SW1A", "GB", "London", false},
		{"Invalid postal code", "99999", "US", "", true},
		{"Invalid country code", "10001", "XX", "", true},
		{"Empty postal code", "", "US", "", true},
		{"Empty country code", "10001", "", "", true},
		{"Both empty", "", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			city := finder.CityByPostalCode(tt.postalCode, tt.countryCode)
			if tt.expectNil {
				assert.Nil(t, city)
			} else {
				assert.NotNil(t, city)
				assert.Equal(t, tt.expectedName, city.Name)
				assert.Equal(t, tt.countryCode, city.Country)
			}
		})
	}
}

func TestCityByPostalCode_EdgeCases(t *testing.T) {
	finder := NewPostalCodeFinder()

	// Test with special characters in postal codes
	specialEntry := dataLoader.PostalCodeEntry{
		CountryCode: "CA",
		PostalCode:  "K1A 0A6", // Canadian postal code with space
		PlaceName:   "Ottawa",
		Latitude:    45.4215,
		Longitude:   -75.6972,
		Accuracy:    1,
	}
	finder.AddPostalCode(specialEntry)

	city := finder.CityByPostalCode("K1A 0A6", "CA")
	assert.NotNil(t, city)
	assert.Equal(t, "Ottawa", city.Name)

	// Test case sensitivity (postal codes should be case sensitive)
	city = finder.CityByPostalCode("k1a 0a6", "CA")
	assert.Nil(t, city)

	city = finder.CityByPostalCode("K1A 0A6", "ca")
	assert.Nil(t, city)
}

func TestSerializeDeserialize(t *testing.T) {
	// Create a temporary file for the index
	tmpfile, err := os.CreateTemp("", "postal_index_test_*.gob")
	assert.NoError(t, err)
	defer func() {
		_ = os.Remove(tmpfile.Name())
	}()

	// Build the initial finder and add test data
	finder := NewPostalCodeFinder()
	for _, entry := range testPostalCodeEntries {
		finder.AddPostalCode(entry)
	}

	// Serialize the finder
	err = finder.SerializeIndex(tmpfile.Name())
	assert.NoError(t, err)

	// Deserialize the finder
	deserializedFinder, err := DeserializeIndex(tmpfile.Name())
	assert.NoError(t, err)
	assert.NotNil(t, deserializedFinder)

	// Test that the deserialized finder works correctly
	city := deserializedFinder.CityByPostalCode("10001", "US")
	assert.NotNil(t, city)
	assert.Equal(t, "New York", city.Name)
	assert.Equal(t, "US", city.Country)
	assert.InDelta(t, 40.7505, city.Latitude, 0.0001)
	assert.InDelta(t, -73.9934, city.Longitude, 0.0001)

	// Test multiple entries
	city = deserializedFinder.CityByPostalCode("90210", "US")
	assert.NotNil(t, city)
	assert.Equal(t, "Beverly Hills", city.Name)

	city = deserializedFinder.CityByPostalCode("M5V", "CA")
	assert.NotNil(t, city)
	assert.Equal(t, "Toronto", city.Name)

	city = deserializedFinder.CityByPostalCode("SW1A", "GB")
	assert.NotNil(t, city)
	assert.Equal(t, "London", city.Name)
}

func TestSerialization_EmptyFinder(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "empty_postal_index_test_*.gob")
	assert.NoError(t, err)
	defer func() {
		_ = os.Remove(tmpfile.Name())
	}()

	// Serialize empty finder
	finder := NewPostalCodeFinder()
	err = finder.SerializeIndex(tmpfile.Name())
	assert.NoError(t, err)

	// Deserialize empty finder
	deserializedFinder, err := DeserializeIndex(tmpfile.Name())
	assert.NoError(t, err)
	assert.NotNil(t, deserializedFinder)

	// Test that lookups return nil for empty finder
	city := deserializedFinder.CityByPostalCode("10001", "US")
	assert.Nil(t, city)
}

func TestDeserializeIndex_FileNotFound(t *testing.T) {
	_, err := DeserializeIndex("nonexistent_file.gob")
	assert.Error(t, err)
}

func TestDeserializeIndex_CorruptFile(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "corrupt_postal_index_test_*.gob")
	assert.NoError(t, err)
	defer func() {
		_ = os.Remove(tmpfile.Name())
	}()

	// Write some garbage data
	_, err = tmpfile.WriteString("this is not gob data")
	assert.NoError(t, err)
	tmpfile.Close()

	// Try to deserialize
	_, err = DeserializeIndex(tmpfile.Name())
	assert.Error(t, err)
}

func TestConcurrentAccess(t *testing.T) {
	finder := NewPostalCodeFinder()

	// Add some test data
	for _, entry := range testPostalCodeEntries {
		finder.AddPostalCode(entry)
	}

	// Test concurrent reads
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				city := finder.CityByPostalCode("10001", "US")
				assert.NotNil(t, city)
				assert.Equal(t, "New York", city.Name)
			}
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestConcurrentReadWrite(t *testing.T) {
	finder := NewPostalCodeFinder()

	// Start multiple goroutines doing reads and writes
	done := make(chan bool, 20)

	// Writers
	for i := 0; i < 5; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				entry := dataLoader.PostalCodeEntry{
					CountryCode: "US",
					PostalCode:  "10001",
					PlaceName:   "New York",
					Latitude:    40.7505,
					Longitude:   -73.9934,
					Accuracy:    1,
				}
				finder.AddPostalCode(entry)
			}
			done <- true
		}(i)
	}

	// Readers
	for i := 0; i < 15; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				city := finder.CityByPostalCode("10001", "US")
				if city != nil {
					assert.Equal(t, "New York", city.Name)
				}
			}
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 20; i++ {
		<-done
	}
}

func TestPostalCodeEntry_ConversionToCity(t *testing.T) {
	entry := dataLoader.PostalCodeEntry{
		CountryCode: "US",
		PostalCode:  "10001",
		PlaceName:   "New York",
		Latitude:    40.7505,
		Longitude:   -73.9934,
		Accuracy:    1,
		AdminName1:  "New York",
		AdminCode1:  "NY",
		AdminName2:  "New York County",
		AdminCode2:  "061",
	}

	finder := NewPostalCodeFinder()
	finder.AddPostalCode(entry)

	city := finder.CityByPostalCode("10001", "US")
	assert.NotNil(t, city)

	// Verify all fields are correctly mapped
	assert.Equal(t, entry.PlaceName, city.Name)
	assert.Equal(t, entry.CountryCode, city.Country)
	assert.Equal(t, entry.Latitude, city.Latitude)
	assert.Equal(t, entry.Longitude, city.Longitude)
}
