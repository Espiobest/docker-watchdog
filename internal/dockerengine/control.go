package dockerengine

import (
	"context"

	"github.com/moby/moby/client"
)

func (e *Engine) Start(ctx context.Context, id string) error {
	_, err := e.Client.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return err
}

func (e *Engine) Stop(ctx context.Context, id string) error {
	timeout := 10
	_, err := e.Client.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &timeout})
	return err
}
