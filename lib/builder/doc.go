// Package builder owns the sequence that turns the raw datasets into the
// three serialized indexes: load the datasets, build the S2 index, build the
// name index on the S2 index's city table, build the postal code index, and
// write the index files.
//
// Both callers run it: lib/initializer (the first boot, and the rebuild of a
// corrupt index) and cmd/build-index (the offline builder). Each calls the
// steps it needs, in the order above, so the initializer can rebuild a single
// index and build-index can time and report every step; the policy of every
// step lives here once. Builds stay sequential (they are the memory peak) and
// writes run concurrently once every index exists, so a failing build leaves
// no half-updated set of files behind.
package builder
