// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package version

var (
	// Version is the current release version.
	Version = "0.1.0"

	// VersionPreRelease is a pre-release marker ("dev", "beta1", etc.).
	// If this is "" then it means that it is a final release. Otherwise,
	// this is a pre-release such as "dev", "beta", "rc1", etc.
	VersionPreRelease = "dev"
)
