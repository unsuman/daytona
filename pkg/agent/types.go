// Copyright 2024 Daytona Platforms Inc.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"io"
	"time"

	"github.com/daytonaio/daytona/pkg/agent/config"
	"github.com/daytonaio/daytona/pkg/docker"
	"github.com/daytonaio/daytona/pkg/git"
	"github.com/daytonaio/daytona/pkg/models"
)

type SshServer interface {
	Start() error
}

type TailscaleServer interface {
	Start() error
}

type ToolboxServer interface {
	Start() error
}

type Agent struct {
	Config           *config.Config
	Git              git.IGitService
	DockerCredHelper docker.IDockerCredHelper
	Ssh              SshServer
	Toolbox          ToolboxServer
	Tailscale        TailscaleServer
	LogWriter        io.Writer
	TelemetryEnabled bool
	startTime        time.Time
	Workspace        *models.Workspace
}
