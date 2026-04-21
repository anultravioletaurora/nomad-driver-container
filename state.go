// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"time"

	"github.com/hashicorp/nomad/plugins/drivers"
)

// taskHandleVersion is used to version the handle schema, and is incremented
// if the TaskState fields are modified, to allow recovery of tasks launched
// by prior driver versions.
const taskHandleVersion = 1

// TaskState is stored in the Nomad handle and is what gets persisted across
// driver plugin restarts. It holds the minimal information needed to reattach
// to a running container.
type TaskState struct {
	// TaskConfig is the Nomad task config at the time the task was started.
	TaskConfig *drivers.TaskConfig

	// ContainerName is the name given to the container instance managed by
	// this driver, in the format "nomad-<taskName>-<allocID[:8]>".
	ContainerName string

	// StartedAt is when the container was started.
	StartedAt time.Time
}
