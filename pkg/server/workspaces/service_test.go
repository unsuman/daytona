// Copyright 2024 Daytona Platforms Inc.
// SPDX-License-Identifier: Apache-2.0

package workspaces_test

import (
	"context"
	"testing"

	"github.com/daytonaio/daytona/internal/testing/job"
	t_targets "github.com/daytonaio/daytona/internal/testing/server/targets"
	"github.com/daytonaio/daytona/internal/testing/server/targets/mocks"
	t_workspaces "github.com/daytonaio/daytona/internal/testing/server/workspaces"
	"github.com/daytonaio/daytona/internal/util"
	"github.com/daytonaio/daytona/pkg/gitprovider"
	"github.com/daytonaio/daytona/pkg/logs"
	"github.com/daytonaio/daytona/pkg/models"
	"github.com/daytonaio/daytona/pkg/server/workspaces"
	"github.com/daytonaio/daytona/pkg/services"
	"github.com/daytonaio/daytona/pkg/stores"
	"github.com/daytonaio/daytona/pkg/telemetry"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const serverApiUrl = "http://localhost:3986"
const serverUrl = "http://localhost:3987"
const serverVersion = "0.0.0-test"
const defaultWorkspaceUser = "daytona"
const defaultWorkspaceImage = "daytonaio/workspace-project:latest"

var gitProviderConfigId = "github"

var baseApiUrl = "https://api.github.com"

var gitProviderConfig = models.GitProviderConfig{
	Id:         "github",
	ProviderId: gitProviderConfigId,
	Alias:      "test-alias",
	Username:   "test-username",
	Token:      "test-token",
	BaseApiUrl: &baseApiUrl,
}

var tc = &models.TargetConfig{
	Name: "tc-test",
	ProviderInfo: models.ProviderInfo{
		Name:    "test-provider",
		Version: "test",
	},
	Options: "test-options",
	Deleted: false,
}

var tg = &models.Target{
	Id:             "123",
	Name:           "test",
	TargetConfigId: tc.Id,
	TargetConfig:   *tc,
}

var createWorkspaceDTO = services.CreateWorkspaceDTO{
	Id:                  "123",
	Name:                "workspace1",
	GitProviderConfigId: &gitProviderConfig.Id,
	Source: services.CreateWorkspaceSourceDTO{
		Repository: &gitprovider.GitRepository{
			Id:     "123",
			Url:    "https://github.com/daytonaio/daytona",
			Name:   "daytona",
			Branch: "main",
			Sha:    "sha1",
		},
	},
	Image:    util.Pointer(defaultWorkspaceImage),
	User:     util.Pointer(defaultWorkspaceUser),
	TargetId: tg.Id,
}

var ws = &models.Workspace{
	Id:                  "123",
	Name:                "workspace1",
	ApiKey:              createWorkspaceDTO.Name,
	GitProviderConfigId: &gitProviderConfig.Id,
	Repository: &gitprovider.GitRepository{
		Id:     "123",
		Url:    "https://github.com/daytonaio/daytona",
		Name:   "daytona",
		Branch: "main",
		Sha:    "sha1",
	},
	Image:    defaultWorkspaceImage,
	User:     defaultWorkspaceUser,
	TargetId: tg.Id,
}

func TestWorkspaceService(t *testing.T) {
	ws.EnvVars = workspaces.GetWorkspaceEnvVars(ws, workspaces.WorkspaceEnvVarParams{
		ApiUrl:    serverApiUrl,
		ServerUrl: serverUrl,
		ClientId:  "test-client-id",
	})

	ctx := context.Background()
	ctx = context.WithValue(ctx, telemetry.CLIENT_ID_CONTEXT_KEY, "test-client-id")

	jobStore := job.NewInMemoryJobStore()

	targetStore := t_targets.NewInMemoryTargetStore(jobStore)
	err := targetStore.Save(ctx, tg)
	require.Nil(t, err)

	workspaceStore := t_workspaces.NewInMemoryWorkspaceStore(jobStore)
	metadataStore := t_workspaces.NewInMemoryWorkspaceMetadataStore()

	apiKeyService := mocks.NewMockApiKeyService()
	gitProviderService := mocks.NewMockGitProviderService()

	tgLogsDir := t.TempDir()

	service := workspaces.NewWorkspaceService(workspaces.WorkspaceServiceConfig{
		WorkspaceMetadataStore: metadataStore,
		FindTarget: func(ctx context.Context, targetId string) (*models.Target, error) {
			t, err := targetStore.Find(ctx, &stores.TargetFilter{IdOrName: &targetId})
			if err != nil {
				return nil, err
			}
			return t, nil
		},
		FindContainerRegistry: func(ctx context.Context, image string, envVars map[string]string) *models.ContainerRegistry {
			return services.EnvironmentVariables(envVars).FindContainerRegistryByImageName(image)
		},
		FindCachedBuild: func(ctx context.Context, w *models.Workspace) (*models.CachedBuild, error) {
			return nil, nil
		},
		CreateApiKey: func(ctx context.Context, name string) (string, error) {
			return apiKeyService.Create(models.ApiKeyTypeWorkspace, name)
		},
		DeleteApiKey: func(ctx context.Context, name string) error {
			return apiKeyService.Delete(name)
		},
		ListGitProviderConfigs: func(ctx context.Context, repoUrl string) ([]*models.GitProviderConfig, error) {
			return gitProviderService.ListConfigsForUrl(repoUrl)
		},
		FindGitProviderConfig: func(ctx context.Context, id string) (*models.GitProviderConfig, error) {
			return gitProviderService.FindConfig(id)
		},
		GetLastCommitSha: func(ctx context.Context, repo *gitprovider.GitRepository) (string, error) {
			return gitProviderService.GetLastCommitSha(repo)
		},
		TrackTelemetryEvent: func(event telemetry.Event, clientId string) error {
			return nil
		},
		WorkspaceStore:        workspaceStore,
		ServerApiUrl:          serverApiUrl,
		ServerUrl:             serverUrl,
		DefaultWorkspaceImage: defaultWorkspaceImage,
		DefaultWorkspaceUser:  defaultWorkspaceUser,
		LoggerFactory:         logs.NewLoggerFactory(logs.LoggerFactoryConfig{LogsDir: tgLogsDir}),
		CreateJob: func(ctx context.Context, workspaceId, runnerId string, action models.JobAction) error {
			return jobStore.Save(ctx, &models.Job{
				Id:           workspaceId,
				ResourceId:   workspaceId,
				RunnerId:     util.Pointer(runnerId),
				ResourceType: models.ResourceTypeWorkspace,
				Action:       action,
				State:        models.JobStateSuccess,
			})
		},
	})

	t.Run("CreateWorkspace", func(t *testing.T) {
		gitProviderService.On("GetLastCommitSha", createWorkspaceDTO.Source.Repository).Return("123", nil)

		apiKeyService.On("Create", models.ApiKeyTypeWorkspace, createWorkspaceDTO.Id).Return(createWorkspaceDTO.Name, nil)

		ws := &models.Workspace{
			Id:                  createWorkspaceDTO.Id,
			Name:                createWorkspaceDTO.Name,
			Image:               *createWorkspaceDTO.Image,
			User:                *createWorkspaceDTO.User,
			BuildConfig:         createWorkspaceDTO.BuildConfig,
			Repository:          createWorkspaceDTO.Source.Repository,
			ApiKey:              createWorkspaceDTO.Name,
			GitProviderConfigId: createWorkspaceDTO.GitProviderConfigId,
			TargetId:            createWorkspaceDTO.Id,
		}

		ws.EnvVars = workspaces.GetWorkspaceEnvVars(ws, workspaces.WorkspaceEnvVarParams{
			ApiUrl:        serverApiUrl,
			ServerUrl:     serverUrl,
			ServerVersion: serverVersion,
			ClientId:      "test",
		})

		gitProviderService.On("FindConfig", "github").Return(&gitProviderConfig, nil)

		workspace, err := service.Create(ctx, createWorkspaceDTO)

		require.Nil(t, err)
		require.NotNil(t, workspace)

		workspaceEquals(t, &services.WorkspaceDTO{Workspace: *ws}, workspace)

		job, err := jobStore.Find(ctx, &stores.JobFilter{ResourceId: &ws.Id})
		require.Nil(t, err)
		require.Equal(t, job.ResourceType, models.ResourceTypeWorkspace)

		ws.EnvVars = nil
	})

	t.Run("UpdateLabels", func(t *testing.T) {
		labels := map[string]string{
			"test": "label",
		}

		workspace, err := service.UpdateLabels(ctx, createWorkspaceDTO.Id, labels)

		require.Nil(t, err)
		require.NotNil(t, workspace)

		require.Equal(t, labels, workspace.Labels)
	})

	t.Run("CreateWorkspace fails when workspace already exists", func(t *testing.T) {
		_, err := service.Create(ctx, createWorkspaceDTO)
		require.NotNil(t, err)
		require.Equal(t, services.ErrWorkspaceAlreadyExists, err)
	})

	t.Run("CreateWorkspace fails name validation", func(t *testing.T) {
		invalidWorkspaceRequest := createWorkspaceDTO
		invalidWorkspaceRequest.Name = "invalid name"

		_, err := service.Create(ctx, invalidWorkspaceRequest)
		require.NotNil(t, err)
		require.Equal(t, services.ErrInvalidWorkspaceName, err)
	})

	t.Run("FindWorkspace", func(t *testing.T) {
		w, err := service.Find(ctx, ws.Id, services.WorkspaceRetrievalParams{})

		require.Nil(t, err)
		require.NotNil(t, w)

		workspaceDtoEquals(t, createWorkspaceDTO, *w, defaultWorkspaceImage)
	})

	t.Run("FindWorkspace fails when workspace not found", func(t *testing.T) {
		_, err := service.Find(ctx, "invalid-id", services.WorkspaceRetrievalParams{})
		require.NotNil(t, err)
		require.Equal(t, stores.ErrWorkspaceNotFound, err)
	})

	t.Run("ListWorkspaces", func(t *testing.T) {
		workspaces, err := service.List(ctx, services.WorkspaceRetrievalParams{})

		require.Nil(t, err)
		require.Len(t, workspaces, 1)

		workspaceDtoEquals(t, createWorkspaceDTO, workspaces[0], defaultWorkspaceImage)
	})

	t.Run("StartWorkspace", func(t *testing.T) {
		err := service.Start(ctx, createWorkspaceDTO.Id)

		require.Nil(t, err)
	})

	t.Run("StopWorkspace", func(t *testing.T) {
		err := service.Stop(ctx, createWorkspaceDTO.Id)

		require.Nil(t, err)
	})

	t.Run("UpdateWorkspaceMetadata", func(t *testing.T) {
		res, err := service.UpdateMetadata(ctx, createWorkspaceDTO.Id, &models.WorkspaceMetadata{
			Uptime: 10,
			GitStatus: &models.GitStatus{
				CurrentBranch: "main",
			},
		})
		require.Nil(t, err)

		require.Nil(t, err)
		require.Equal(t, "main", res.GitStatus.CurrentBranch)
	})

	t.Run("DeleteWorkspace", func(t *testing.T) {
		apiKeyService.On("Delete", mock.Anything).Return(nil)

		err := service.Delete(ctx, createWorkspaceDTO.Id)

		require.Nil(t, err)

		_, err = service.Find(ctx, createWorkspaceDTO.Id, services.WorkspaceRetrievalParams{})
		require.Equal(t, services.ErrWorkspaceDeleted, err)
	})

	t.Run("ForceDeleteWorkspace", func(t *testing.T) {
		err := workspaceStore.Save(ctx, ws)
		require.Nil(t, err)

		apiKeyService.On("Delete", mock.Anything).Return(nil)

		err = service.ForceDelete(ctx, createWorkspaceDTO.Id)

		require.Nil(t, err)

		_, err = service.Find(ctx, createWorkspaceDTO.Id, services.WorkspaceRetrievalParams{})
		require.Equal(t, services.ErrWorkspaceDeleted, err)
	})

	t.Cleanup(func() {
		apiKeyService.AssertExpectations(t)
	})
}

func workspaceEquals(t *testing.T, ws1, ws2 *services.WorkspaceDTO) {
	t.Helper()

	require.Equal(t, ws1.Id, ws2.Id)
	require.Equal(t, ws1.Name, ws2.Name)
	require.Equal(t, ws1.TargetId, ws2.TargetId)
	require.Equal(t, ws1.Image, ws2.Image)
	require.Equal(t, ws1.User, ws2.User)
	require.Equal(t, ws1.ApiKey, ws2.ApiKey)
	require.Equal(t, ws1.Repository.Id, ws2.Repository.Id)
	require.Equal(t, ws1.Repository.Url, ws2.Repository.Url)
	require.Equal(t, ws1.Repository.Name, ws2.Repository.Name)
}

func workspaceDtoEquals(t *testing.T, req services.CreateWorkspaceDTO, workspace services.WorkspaceDTO, workspaceImage string) {
	t.Helper()

	require.Equal(t, req.Id, workspace.Id)
	require.Equal(t, req.Name, workspace.Name)

	require.Equal(t, req.Name, workspace.Name)
	require.Equal(t, req.Source.Repository.Id, workspace.Repository.Id)
	require.Equal(t, req.Source.Repository.Url, workspace.Repository.Url)
	require.Equal(t, req.Source.Repository.Name, workspace.Repository.Name)
	require.Equal(t, workspace.ApiKey, workspace.Name)
	require.Equal(t, workspace.Image, workspaceImage)
}
