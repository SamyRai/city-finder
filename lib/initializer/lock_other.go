//go:build !unix

package initializer

import "os"

// lockExclusive is a no-op on non-unix platforms (the server targets Linux
// containers; darwin is covered by the unix build): concurrent initializers
// are not serialized there.
func lockExclusive(*os.File) error { return nil }
