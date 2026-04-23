// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"os"

	hclog "github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	"github.com/hashicorp/nomad/plugins/base"
	"github.com/hashicorp/nomad/plugins/drivers"
)

func main() {
	logger := hclog.New(&hclog.LoggerOptions{
		Name:       PluginName,
		Level:      hclog.LevelFromString(logLevel()),
		JSONFormat: true,
		Output:     os.Stderr,
	})

	// Serve the plugin.  This call blocks until the plugin process is killed
	// or the parent Nomad client terminates.
	d := NewDriver(logger)
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: base.Handshake,
		Plugins: map[string]goplugin.Plugin{
			base.PluginTypeBase:   &base.PluginBase{Impl: d},
			base.PluginTypeDriver: drivers.NewDriverPlugin(d, logger),
		},
		GRPCServer: goplugin.DefaultGRPCServer,
		Logger:     logger,
	})
}

// logLevel reads the NOMAD_PLUGIN_LOG_LEVEL environment variable and falls
// back to "INFO" if it is not set or not a valid level.
func logLevel() string {
	if lvl := os.Getenv("NOMAD_PLUGIN_LOG_LEVEL"); lvl != "" {
		return lvl
	}
	return "INFO"
}
