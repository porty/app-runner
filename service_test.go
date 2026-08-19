package main

import (
	"context"
	"errors"
	"testing"
	"time"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
	"github.com/twitchtv/twirp"
)

func TestPing(t *testing.T) {
	service := newAppRunnerService(nil, nil)
	service.now = func() time.Time {
		return time.Date(2026, time.August, 19, 5, 30, 0, 0, time.UTC)
	}

	response, err := service.Ping(context.Background(), &apprunnerv1.PingRequest{})
	if err != nil {
		t.Fatalf("Ping returned an error: %v", err)
	}
	if response.GetMessage() != "App Runner backend is ready" {
		t.Fatalf("unexpected message: %q", response.GetMessage())
	}
	if response.GetServerTime() != "2026-08-19T05:30:00Z" {
		t.Fatalf("unexpected server time: %q", response.GetServerTime())
	}
}

func TestEchoRejectsEmptyMessages(t *testing.T) {
	service := newAppRunnerService(nil, nil)

	_, err := service.Echo(context.Background(), &apprunnerv1.EchoRequest{Message: "  "})
	var twirpError twirp.Error
	if !errors.As(err, &twirpError) || twirpError.Code() != twirp.InvalidArgument {
		t.Fatalf("expected invalid_argument, got %v", err)
	}
}
