package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResolveVersionBuild tests that when the build-time version variable
// is set (via -ldflags "-X main.version=..."), resolveVersion returns it.
func TestResolveVersionBuild(t *testing.T) {
	// Arrange: set the package-level version variable
	version = "v1.2.3-devel"

	// Act
	result := resolveVersion()

	// Assert
	require.Equal(t, "v1.2.3-devel", result)
}

// TestResolveVersionBuildInfo tests that when the build-time version variable
// is empty, resolveVersion returns the module version from build info.
// The result is never empty - it's either the build-info version or "devel".
func TestResolveVersionBuildInfo(t *testing.T) {
	// Arrange: clear the version variable
	version = ""

	// Act
	result := resolveVersion()

	// Assert
	// When version is empty, resolveVersion falls back to the module version
	// from build info, or "devel" if neither is available.
	// The result is never empty.
	require.NotEmpty(t, result)
}

// TestResolveVersionNeverEmpty tests that resolveVersion always returns a
// non-empty string, preferring: build-time variable > build info > "devel".
func TestResolveVersionNeverEmpty(t *testing.T) {
	// Arrange: clear the version variable
	version = ""

	// Act
	result := resolveVersion()

	// Assert
	// resolveVersion should never return an empty string.
	// It prefers build-time version, then module version from build info, then "devel".
	require.NotEmpty(t, result)
}
