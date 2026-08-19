package main

import (
	"context"
	"strings"
	"time"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
	"github.com/twitchtv/twirp"
)

type appRunnerService struct {
	now func() time.Time
}

func newAppRunnerService() *appRunnerService {
	return &appRunnerService{now: time.Now}
}

func (s *appRunnerService) Ping(context.Context, *apprunnerv1.PingRequest) (*apprunnerv1.PingResponse, error) {
	return &apprunnerv1.PingResponse{
		Message:    "App Runner backend is ready",
		ServerTime: s.now().UTC().Format(time.RFC3339),
	}, nil
}

func (s *appRunnerService) Echo(_ context.Context, request *apprunnerv1.EchoRequest) (*apprunnerv1.EchoResponse, error) {
	message := strings.TrimSpace(request.GetMessage())
	if message == "" {
		return nil, twirp.InvalidArgumentError("message", "a message is required")
	}

	return &apprunnerv1.EchoResponse{Message: message}, nil
}
