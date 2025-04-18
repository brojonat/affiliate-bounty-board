package main

import (
	"log/slog"

	"github.com/brojonat/affiliate-bounty-board/worker"
	"github.com/urfave/cli/v2"
)

func workerCommands() []*cli.Command {
	return []*cli.Command{
		{
			Name:  "worker",
			Usage: "Run the worker",
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:    "temporal-address",
					Aliases: []string{"ta"},
					Usage:   "Temporal server address",
					EnvVars: []string{"TEMPORAL_ADDRESS"},
					Value:   "localhost:7233",
				},
				&cli.StringFlag{
					Name:    "temporal-namespace",
					Aliases: []string{"tn"},
					Usage:   "Temporal namespace",
					EnvVars: []string{"TEMPORAL_NAMESPACE"},
					Value:   "default",
				},
				&cli.BoolFlag{
					Name:    "local-mode",
					Aliases: []string{"l"},
					Usage:   "Run in local mode without Solana functionality",
					Value:   false,
				},
			},
			Action: run_worker,
		},
	}
}

func run_worker(c *cli.Context) error {
	localMode := c.Bool("local-mode")

	if localMode {
		return worker.RunWorkerLocal(
			c.Context,
			getDefaultLogger(slog.LevelInfo),
			c.String("temporal-address"),
			c.String("temporal-namespace"),
		)
	}

	return worker.RunWorker(
		c.Context,
		getDefaultLogger(slog.LevelInfo),
		c.String("temporal-address"),
		c.String("temporal-namespace"),
	)
}
