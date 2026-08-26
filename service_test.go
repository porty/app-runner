package apprunner

import (
	"context"
	"testing"
	"time"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
)

func TestCloudInitTemplateRPCs(t *testing.T) {
	manager, err := newCloudInitTemplateManager(&memoryCloudInitTemplateStore{})
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return time.Date(2026, time.August, 24, 5, 0, 0, 0, time.UTC) }
	service := newAppRunnerService(nil, nil)
	service.templates = manager

	created, err := service.CreateCloudInitTemplate(context.Background(), &apprunnerv1.CreateCloudInitTemplateRequest{
		Name: "Ubuntu", UserData: "#cloud-config\n",
	})
	if err != nil || created.GetTemplate().GetName() != "Ubuntu" || created.GetTemplate().GetCreatedAt() != "2026-08-24T05:00:00Z" {
		t.Fatalf("create template = %#v, %v", created, err)
	}
	listed, err := service.ListCloudInitTemplates(context.Background(), &apprunnerv1.ListCloudInitTemplatesRequest{})
	if err != nil || len(listed.GetTemplates()) != 1 || listed.GetTemplates()[0].GetUserData() != "#cloud-config\n" {
		t.Fatalf("list templates = %#v, %v", listed, err)
	}
	updated, err := service.UpdateCloudInitTemplate(context.Background(), &apprunnerv1.UpdateCloudInitTemplateRequest{
		Name: "Ubuntu", NewName: "Ubuntu server", UserData: "#!/bin/sh\n",
	})
	if err != nil || updated.GetTemplate().GetName() != "Ubuntu server" {
		t.Fatalf("update template = %#v, %v", updated, err)
	}
	if _, err := service.DeleteCloudInitTemplate(context.Background(), &apprunnerv1.DeleteCloudInitTemplateRequest{Name: "Ubuntu server"}); err != nil {
		t.Fatal(err)
	}
}
