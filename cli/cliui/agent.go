package cliui

import (
	"context"
	"io"
	"time"

	"github.com/google/uuid"
	"golang.org/x/xerrors"

	"github.com/coder/coder/codersdk"
)

var (
	AgentStartError   = xerrors.New("agent startup exited with non-zero exit status")
	AgentShuttingDown = xerrors.New("agent is shutting down")
)

type AgentOptions struct {
	WorkspaceName string
	Fetch         func(context.Context) (codersdk.WorkspaceAgent, error)
	FetchLogs     func(ctx context.Context, agentID uuid.UUID, after int64, follow bool) (<-chan []codersdk.WorkspaceAgentStartupLog, io.Closer, error)
	FetchInterval time.Duration
	WarnInterval  time.Duration
	Wait          bool // If true, wait for the agent to be ready (startup script).
}

// Agent displays a spinning indicator that waits for a workspace agent to connect.
func Agent(ctx context.Context, writer io.Writer, opts AgentOptions) error {
	if opts.FetchInterval == 0 {
		opts.FetchInterval = 500 * time.Millisecond
	}
	if opts.WarnInterval == 0 {
		opts.WarnInterval = 30 * time.Second
	}

	agent, err := opts.Fetch(ctx)
	if err != nil {
		return xerrors.Errorf("fetch: %w", err)
	}

	// TODO(mafredri): Rewrite this, we don't want to spam a fetch after every break statement.
	fetch := func(err *error) bool {
		agent, *err = opts.Fetch(ctx)
		return *err == nil
	}

	sw := &stageWriter{w: writer}

	showInitialConnection := true
	showStartupLogs := false

	printInitialConnection := func() error {
		showInitialConnection = false

		// Since we were waiting for the agent to connect, also show
		// startup logs.
		showStartupLogs = true

		stage := "Waiting for initial connection from the workspace agent"
		sw.Start(stage)
		if agent.Status == codersdk.WorkspaceAgentConnecting {
			for fetch(&err) {
				if agent.Status != codersdk.WorkspaceAgentConnecting {
					break
				}
				time.Sleep(opts.FetchInterval)
			}
			if err != nil {
				return xerrors.Errorf("fetch: %w", err)
			}
		}
		if agent.Status == codersdk.WorkspaceAgentTimeout {
			now := time.Now()
			sw.Log(now, codersdk.LogLevelInfo, "The workspace agent is having trouble connecting, we will keep trying to reach it")
			sw.Log(now, codersdk.LogLevelInfo, "For more information and troubleshooting, see https://coder.com/docs/v2/latest/templates#agent-connection-issues")
			for fetch(&err) {
				if agent.Status != codersdk.WorkspaceAgentConnecting && agent.Status != codersdk.WorkspaceAgentTimeout {
					break
				}
				time.Sleep(opts.FetchInterval)
			}
			if err != nil {
				return xerrors.Errorf("fetch: %w", err)
			}
		}
		sw.Complete(stage, agent.FirstConnectedAt.Sub(agent.CreatedAt))
		return nil
	}

	printLogs := func(follow bool) error {
		logStream, logsCloser, err := opts.FetchLogs(ctx, agent.ID, 0, follow)
		if err != nil {
			return xerrors.Errorf("fetch logs: %w", err)
		}
		defer logsCloser.Close()

		for logs := range logStream {
			for _, log := range logs {
				sw.Log(log.CreatedAt, log.Level, log.Output)
			}
		}

		return nil
	}

	for {
		// TODO(mafredri): Handle shutting down lifecycle states.

		switch agent.Status {
		case codersdk.WorkspaceAgentConnecting, codersdk.WorkspaceAgentTimeout:
			err := printInitialConnection()
			if err != nil {
				return xerrors.Errorf("initial connection: %w", err)
			}

		case codersdk.WorkspaceAgentConnected:
			if !showStartupLogs && agent.LifecycleState == codersdk.WorkspaceAgentLifecycleReady {
				// The workspace is ready, there's nothing to do but connect.
				return nil
			}
			if showInitialConnection {
				err := printInitialConnection()
				if err != nil {
					return xerrors.Errorf("initial connection: %w", err)
				}
			}

			stage := "Running workspace agent startup script"
			follow := opts.Wait
			if !follow {
				stage += " (non-blocking)"
			}
			sw.Start(stage)

			err = printLogs(follow)
			if err != nil {
				return xerrors.Errorf("print logs: %w", err)
			}

			for fetch(&err) {
				if !follow || !agent.LifecycleState.Starting() {
					break
				}
				time.Sleep(opts.FetchInterval)
			}
			if err != nil {
				return xerrors.Errorf("fetch: %w", err)
			}

			switch agent.LifecycleState {
			case codersdk.WorkspaceAgentLifecycleReady:
				sw.Complete(stage, agent.ReadyAt.Sub(*agent.StartedAt))
			case codersdk.WorkspaceAgentLifecycleStartError:
				sw.Log(time.Time{}, codersdk.LogLevelWarn, "Warning: The startup script exited with an error and your workspace may be incomplete.")
				sw.Log(time.Time{}, codersdk.LogLevelWarn, "For more information and troubleshooting, see https://coder.com/docs/v2/latest/templates#startup-script-exited-with-an-error")
				sw.Fail(stage, agent.ReadyAt.Sub(*agent.StartedAt))
			default:
				sw.Log(time.Time{}, codersdk.LogLevelWarn, "Notice: The startup script is still running and your workspace may be incomplete.")
				sw.Log(time.Time{}, codersdk.LogLevelWarn, "For more information and troubleshooting, see https://coder.com/docs/v2/latest/templates#your-workspace-may-be-incomplete")
				// Note: We don't complete or fail the stage here, it's
				// intentionally left open to indicate this stage never
				// completed.
			}

			return nil

		case codersdk.WorkspaceAgentDisconnected:
			// Since we were waiting for the agent to reconnect, also show
			// startup logs.
			showStartupLogs = true
			showInitialConnection = false

			stage := "The workspace agent lost connection, waiting for it to reconnect"
			sw.Start(stage)
			for fetch(&err) {
				if agent.Status != codersdk.WorkspaceAgentDisconnected {
					break
				}
			}
			if err != nil {
				return xerrors.Errorf("fetch: %w", err)
			}
			sw.Complete(stage, agent.LastConnectedAt.Sub(*agent.DisconnectedAt))
		}
	}
}
