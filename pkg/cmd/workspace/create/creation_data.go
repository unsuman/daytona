// Copyright 2024 Daytona Platforms Inc.
// SPDX-License-Identifier: Apache-2.0

package create

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	apiclient_util "github.com/daytonaio/daytona/internal/util/apiclient"
	"github.com/daytonaio/daytona/pkg/apiclient"
	cmd_common "github.com/daytonaio/daytona/pkg/cmd/common"
	"github.com/daytonaio/daytona/pkg/common"
	"github.com/daytonaio/daytona/pkg/views/selection"
	views_util "github.com/daytonaio/daytona/pkg/views/util"
	"github.com/daytonaio/daytona/pkg/views/workspace/create"
	"github.com/docker/docker/pkg/stringid"
)

type WorkspacesDataPromptParams struct {
	UserGitProviders    []apiclient.GitProvider
	WorkspaceTemplates  []apiclient.WorkspaceTemplate
	Manual              bool
	SkipBranchSelection bool
	MultiWorkspace      bool
	BlankWorkspace      bool
	ApiClient           *apiclient.APIClient
	Defaults            *views_util.WorkspaceTemplateDefaults
}

func GetWorkspacesCreationDataFromPrompt(ctx context.Context, params WorkspacesDataPromptParams) ([]apiclient.CreateWorkspaceDTO, error) {
	var workspaceList []apiclient.CreateWorkspaceDTO
	// keep track of visited repos, will help in keeping workspace names unique
	// since these are later saved into the db under a unique constraint field.
	selectedRepos := make(map[string]int)

	for i := 1; params.MultiWorkspace || i == 1; i++ {
		var err error

		if i > 2 {
			addMore, err := create.RunAddMoreWorkspacesForm()
			if err != nil {
				return nil, err
			}
			if !addMore {
				break
			}
		}

		if len(params.WorkspaceTemplates) > 0 && !params.BlankWorkspace {
			workspaceTemplate := selection.GetWorkspaceTemplateFromPrompt(params.WorkspaceTemplates, i, true, false, "Use")
			if workspaceTemplate == nil {
				return nil, common.ErrCtrlCAbort
			}

			workspaceNames := []string{}
			for _, w := range workspaceList {
				workspaceNames = append(workspaceNames, w.Name)
			}

			// Append occurence number to keep duplicate entries unique
			repoUrl := workspaceTemplate.RepositoryUrl
			if len(selectedRepos) > 0 && selectedRepos[repoUrl] > 1 {
				workspaceTemplate.Name += strconv.Itoa(selectedRepos[repoUrl])
			}

			if workspaceTemplate.Name != selection.BlankWorkspaceIdentifier {
				workspaceName := GetSuggestedName(workspaceTemplate.Name, workspaceNames)

				getRepoContext := apiclient.GetRepositoryContext{
					Url: workspaceTemplate.RepositoryUrl,
				}

				branch, err := GetBranchFromWorkspaceTemplate(ctx, workspaceTemplate, params.ApiClient, i)
				if err != nil {
					return nil, err
				}

				if branch != nil {
					getRepoContext.Branch = &branch.Name
					getRepoContext.Sha = &branch.Sha
				}

				templateRepo, res, err := params.ApiClient.GitProviderAPI.GetGitContext(ctx).Repository(getRepoContext).Execute()
				if err != nil {
					return nil, apiclient_util.HandleErrorResponse(res, err)
				}

				createWorkspaceDto := apiclient.CreateWorkspaceDTO{
					Name:                workspaceName,
					GitProviderConfigId: workspaceTemplate.GitProviderConfigId,
					Source: apiclient.CreateWorkspaceSourceDTO{
						Repository: *templateRepo,
					},
					BuildConfig: workspaceTemplate.BuildConfig,
					Image:       params.Defaults.Image,
					User:        params.Defaults.ImageUser,
					EnvVars:     workspaceTemplate.EnvVars,
					Labels:      workspaceTemplate.Labels,
				}

				if workspaceTemplate.Image != "" {
					createWorkspaceDto.Image = &workspaceTemplate.Image
				}

				if workspaceTemplate.User != "" {
					createWorkspaceDto.User = &workspaceTemplate.User
				}

				if workspaceTemplate.GitProviderConfigId == nil || *workspaceTemplate.GitProviderConfigId == "" {
					gitProviderConfigId, res, err := params.ApiClient.GitProviderAPI.FindGitProviderIdForUrl(ctx, url.QueryEscape(workspaceTemplate.RepositoryUrl)).Execute()
					if err != nil {
						return nil, apiclient_util.HandleErrorResponse(res, err)
					}
					createWorkspaceDto.GitProviderConfigId = &gitProviderConfigId
				}

				workspaceList = append(workspaceList, createWorkspaceDto)
				continue
			}
		}

		providerRepo, gitProviderConfigId, err := getRepositoryFromWizard(ctx, RepositoryWizardParams{
			ApiClient:           params.ApiClient,
			UserGitProviders:    params.UserGitProviders,
			Manual:              params.Manual,
			MultiWorkspace:      params.MultiWorkspace,
			SkipBranchSelection: params.SkipBranchSelection,
			WorkspaceOrder:      i,
			SelectedRepos:       selectedRepos,
		})
		if err != nil {
			return nil, err
		}

		if gitProviderConfigId == selection.CustomRepoIdentifier || gitProviderConfigId == selection.CREATE_FROM_SAMPLE {
			gitProviderConfigs, res, err := params.ApiClient.GitProviderAPI.ListGitProvidersForUrl(ctx, url.QueryEscape(providerRepo.Url)).Execute()
			if err != nil {
				return nil, apiclient_util.HandleErrorResponse(res, err)
			}

			if len(gitProviderConfigs) == 1 {
				gitProviderConfigId = gitProviderConfigs[0].Id
			} else if len(gitProviderConfigs) > 1 {
				gp := selection.GetGitProviderConfigFromPrompt(selection.GetGitProviderConfigParams{
					GitProviderConfigs: gitProviderConfigs,
					ActionVerb:         "Use",
				})
				if gp == nil {
					return nil, common.ErrCtrlCAbort
				}
				gitProviderConfigId = gp.Id
			} else {
				gitProviderConfigId = ""
			}
		}

		getRepoContext := createGetRepoContextFromRepository(providerRepo)

		var res *http.Response
		providerRepo, res, err = params.ApiClient.GitProviderAPI.GetGitContext(ctx).Repository(getRepoContext).Execute()
		if err != nil {
			return nil, apiclient_util.HandleErrorResponse(res, err)
		}

		providerRepoName, err := GetSanitizedWorkspaceName(providerRepo.Name)
		if err != nil {
			return nil, err
		}

		workspaceList = append(workspaceList, newCreateWorkspaceTemplateDTO(params, providerRepo, providerRepoName, gitProviderConfigId))
	}

	return workspaceList, nil
}

func GetWorkspaceNameFromRepo(repoUrl string) string {
	workspaceNameSlugRegex := regexp.MustCompile(`[^a-zA-Z0-9-]`)
	return workspaceNameSlugRegex.ReplaceAllString(strings.TrimSuffix(strings.ToLower(filepath.Base(repoUrl)), ".git"), "-")
}

func GetSuggestedName(initialSuggestion string, existingNames []string) string {
	suggestion := initialSuggestion

	if !slices.Contains(existingNames, suggestion) {
		return suggestion
	} else {
		i := 2
		for {
			newSuggestion := fmt.Sprintf("%s%d", suggestion, i)
			if !slices.Contains(existingNames, newSuggestion) {
				return newSuggestion
			}
			i++
		}
	}
}

func GetSanitizedWorkspaceName(workspaceName string) (string, error) {
	workspaceName, err := url.QueryUnescape(workspaceName)
	if err != nil {
		return "", err
	}
	workspaceName = strings.ReplaceAll(workspaceName, " ", "-")

	return workspaceName, nil
}

func GetBranchFromWorkspaceTemplate(ctx context.Context, workspaceTemplate *apiclient.WorkspaceTemplate, apiClient *apiclient.APIClient, workspaceOrder int) (*apiclient.GitBranch, error) {
	encodedURLParam := url.QueryEscape(workspaceTemplate.RepositoryUrl)

	repoResponse, res, err := apiClient.GitProviderAPI.GetGitContext(ctx).Repository(apiclient.GetRepositoryContext{
		Url: workspaceTemplate.RepositoryUrl,
	}).Execute()
	if err != nil {
		return nil, apiclient_util.HandleErrorResponse(res, err)
	}

	gitProviderConfigId, res, err := apiClient.GitProviderAPI.FindGitProviderIdForUrl(ctx, encodedURLParam).Execute()
	if err != nil {
		return nil, apiclient_util.HandleErrorResponse(res, err)
	}

	gitProvider, _, err := apiClient.GitProviderAPI.FindGitProvider(ctx, gitProviderConfigId).Execute()
	if err == nil && gitProvider != nil {
		gitProviderConfigId = gitProvider.ProviderId
	}

	branchWizardConfig := BranchWizardParams{
		ApiClient:           apiClient,
		GitProviderConfigId: gitProviderConfigId,
		NamespaceId:         repoResponse.Owner,
		ChosenRepo:          repoResponse,
		WorkspaceOrder:      workspaceOrder,
	}

	repo, err := SetBranchFromWizard(branchWizardConfig)
	if err != nil {
		return nil, err
	}

	if repo == nil {
		return nil, common.ErrCtrlCAbort
	}

	return &apiclient.GitBranch{
		Name: repo.Branch,
		Sha:  repo.Sha,
	}, nil
}

func GetCreateWorkspaceDtoFromFlags(workspaceConfigurationFlags cmd_common.WorkspaceConfigurationFlags) (*apiclient.CreateWorkspaceDTO, error) {
	workspace := &apiclient.CreateWorkspaceDTO{
		GitProviderConfigId: workspaceConfigurationFlags.GitProviderConfig,
		BuildConfig:         &apiclient.BuildConfig{},
	}

	if *workspaceConfigurationFlags.Builder == views_util.DEVCONTAINER || *workspaceConfigurationFlags.DevcontainerPath != "" {
		devcontainerFilePath := create.DEVCONTAINER_FILEPATH
		if *workspaceConfigurationFlags.DevcontainerPath != "" {
			devcontainerFilePath = *workspaceConfigurationFlags.DevcontainerPath
		}
		workspace.BuildConfig.Devcontainer = &apiclient.DevcontainerConfig{
			FilePath: devcontainerFilePath,
		}

	}

	if *workspaceConfigurationFlags.Builder == views_util.NONE || *workspaceConfigurationFlags.CustomImage != "" || *workspaceConfigurationFlags.CustomImageUser != "" {
		workspace.BuildConfig = nil
		if *workspaceConfigurationFlags.CustomImage != "" || *workspaceConfigurationFlags.CustomImageUser != "" {
			workspace.Image = workspaceConfigurationFlags.CustomImage
			workspace.User = workspaceConfigurationFlags.CustomImageUser
		}
	}

	envVars := make(map[string]string)

	if workspaceConfigurationFlags.EnvVars != nil {
		var err error
		envVars, err = cmd_common.MapKeyValue(*workspaceConfigurationFlags.EnvVars)
		if err != nil {
			return nil, err
		}
	}

	labels := make(map[string]string)

	if workspaceConfigurationFlags.Labels != nil {
		var err error
		labels, err = cmd_common.MapKeyValue(*workspaceConfigurationFlags.Labels)
		if err != nil {
			return nil, err
		}
	}

	workspace.EnvVars = envVars
	workspace.Labels = labels

	return workspace, nil
}

func GetGitProviderConfigIdFromFlag(ctx context.Context, apiClient *apiclient.APIClient, gitProviderConfigFlag *string) (*string, error) {
	if gitProviderConfigFlag == nil || *gitProviderConfigFlag == "" {
		return gitProviderConfigFlag, nil
	}

	gitProviderConfigs, res, err := apiClient.GitProviderAPI.ListGitProviders(ctx).Execute()
	if err != nil {
		return nil, apiclient_util.HandleErrorResponse(res, err)
	}

	for _, gitProviderConfig := range gitProviderConfigs {
		if gitProviderConfig.Id == *gitProviderConfigFlag {
			return &gitProviderConfig.Id, nil
		}
		if gitProviderConfig.Alias == *gitProviderConfigFlag {
			return &gitProviderConfig.Id, nil
		}
	}

	return nil, fmt.Errorf("git provider config '%s' not found", *gitProviderConfigFlag)
}

func newCreateWorkspaceTemplateDTO(params WorkspacesDataPromptParams, providerRepo *apiclient.GitRepository, providerRepoName string, gitProviderConfigId string) apiclient.CreateWorkspaceDTO {
	workspace := apiclient.CreateWorkspaceDTO{
		Name:                providerRepoName,
		GitProviderConfigId: &gitProviderConfigId,
		Source: apiclient.CreateWorkspaceSourceDTO{
			Repository: *providerRepo,
		},
		BuildConfig: &apiclient.BuildConfig{},
		Image:       params.Defaults.Image,
		User:        params.Defaults.ImageUser,
		EnvVars:     map[string]string{},
		Labels:      map[string]string{},
	}

	return workspace
}

func createGetRepoContextFromRepository(providerRepo *apiclient.GitRepository) apiclient.GetRepositoryContext {
	result := apiclient.GetRepositoryContext{
		Id:     &providerRepo.Id,
		Name:   &providerRepo.Name,
		Owner:  &providerRepo.Owner,
		Sha:    &providerRepo.Sha,
		Source: &providerRepo.Source,
		Url:    providerRepo.Url,
		Branch: &providerRepo.Branch,
	}

	if providerRepo.Path != nil {
		result.Path = providerRepo.Path
	}

	return result
}

func setInitialWorkspaceNames(createWorkspaceDtos *[]apiclient.CreateWorkspaceDTO, existingWorkspaces []apiclient.WorkspaceDTO) {
	existingNames := make(map[string]bool)
	for _, workspace := range existingWorkspaces {
		existingNames[workspace.Name] = true
	}

	for i := range *createWorkspaceDtos {
		originalName := (*createWorkspaceDtos)[i].Name
		newName := originalName
		counter := 2

		for existingNames[newName] {
			newName = fmt.Sprintf("%s%d", originalName, counter)
			counter++
		}

		(*createWorkspaceDtos)[i].Name = newName
		existingNames[newName] = true
	}
}

func generateWorkspaceIds(createWorkspaceDtos *[]apiclient.CreateWorkspaceDTO) []string {
	for i := range *createWorkspaceDtos {
		wsId := stringid.GenerateRandomID()
		wsId = stringid.TruncateID(wsId)
		(*createWorkspaceDtos)[i].Id = wsId
	}

	return nil
}
