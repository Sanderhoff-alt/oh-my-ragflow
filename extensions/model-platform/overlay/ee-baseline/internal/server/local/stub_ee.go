//go:build ee

package local

// IsAdminAvailable reports whether the admin server may serve requests. The
// community edition runs without one.
func IsAdminAvailable() bool { return true }
