// Copyright 2024 Daytona Platforms Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/daytonaio/daytona/pkg/build/detect"
	"github.com/daytonaio/daytona/pkg/git"
	"github.com/daytonaio/daytona/pkg/models"
	"github.com/daytonaio/daytona/pkg/ssh"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	log "github.com/sirupsen/logrus"
)

func (d *DockerClient) CreateTarget(target *models.Target, targetDir string, logWriter io.Writer, sshClient *ssh.Client) error {
	var err error
	if sshClient == nil {
		err = os.MkdirAll(targetDir, 0755)
	} else {
		err = sshClient.Exec(fmt.Sprintf("mkdir -p %s", targetDir), nil)
	}

	return err
}

func (d *DockerClient) CreateWorkspace(opts *CreateWorkspaceOptions) error {
	// pulledImages map keeps track of pulled images for workspace creation in order to avoid pulling the same image multiple times
	// This is only an optimisation for images with tag 'latest'
	pulledImages := map[string]bool{}

	if opts.Workspace.BuildConfig != nil {
		cr := opts.ContainerRegistries.FindContainerRegistryByImageName(opts.BuilderImage)
		err := d.PullImage(opts.BuilderImage, cr, opts.LogWriter)
		if err != nil {
			return err
		}
		pulledImages[opts.BuilderImage] = true

		err = d.cloneWorkspaceRepository(opts)
		if err != nil {
			return err
		}

		builderType, err := detect.DetectWorkspaceBuilderType(opts.Workspace.BuildConfig, opts.WorkspaceDir, opts.SshClient)
		if err != nil {
			return err
		}

		switch builderType {
		case detect.BuilderTypeDevcontainer:
			_, _, err := d.CreateFromDevcontainer(d.toCreateDevcontainerOptions(opts, true))
			return err
		case detect.BuilderTypeImage:
			return d.createWorkspaceFromImage(opts, pulledImages, true)
		default:
			return fmt.Errorf("unknown builder type: %s", builderType)
		}
	}

	return d.createWorkspaceFromImage(opts, pulledImages, false)
}

func (d *DockerClient) cloneWorkspaceRepository(opts *CreateWorkspaceOptions) error {
	ctx := context.Background()

	if opts.SshClient != nil {
		err := opts.SshClient.Exec(fmt.Sprintf("mkdir -p %s", opts.WorkspaceDir), nil)
		if err != nil {
			return err
		}
	} else {
		err := os.MkdirAll(opts.WorkspaceDir, 0755)
		if err != nil {
			return err
		}
	}

	var auth *http.BasicAuth
	if opts.Gpc != nil {
		auth = &http.BasicAuth{
			Username: opts.Gpc.Username,
			Password: opts.Gpc.Token,
		}
	}

	gitService := git.Service{
		WorkspaceDir: fmt.Sprintf("/workdir/%s", opts.Workspace.WorkspaceFolderName()),
	}

	cloneCmd := gitService.CloneRepositoryCmd(opts.Workspace.Repository, auth)

	c, err := d.apiClient.ContainerCreate(ctx, &container.Config{
		Image:      opts.BuilderImage,
		Entrypoint: []string{"sleep"},
		Cmd:        []string{"infinity"},
		Env: []string{
			"GIT_SSL_NO_VERIFY=true",
		},
	}, &container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: filepath.Dir(opts.WorkspaceDir),
				Target: "/workdir",
			},
		},
	}, nil, nil, fmt.Sprintf("git-clone-%s-%s", opts.Workspace.TargetId, opts.Workspace.Name))
	if err != nil {
		return err
	}

	defer d.RemoveContainer(c.ID) // nolint:errcheck

	err = d.apiClient.ContainerStart(ctx, c.ID, container.StartOptions{})
	if err != nil {
		return err
	}

	go func() {
		for {
			err = d.GetContainerLogs(c.ID, opts.LogWriter)
			if err == nil {
				break
			}
			log.Error(err)
			time.Sleep(100 * time.Millisecond)
		}
	}()

	containerUser := "daytona"

	if runtime.GOOS != "windows" {
		containerUser, err = d.updateContainerUserUidGid(c.ID, opts)
	}

	res, err := d.ExecSync(c.ID, container.ExecOptions{
		User: containerUser,
		Cmd:  append([]string{"sh", "-c"}, strings.Join(cloneCmd, " ")),
	}, opts.LogWriter)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("git clone failed with exit code %d", res.ExitCode)
	}

	return nil
}

func (d *DockerClient) updateContainerUserUidGid(containerId string, opts *CreateWorkspaceOptions) (string, error) {
	currentUser, err := user.Current()
	if err != nil {
		return "", err
	}

	containerUser := "daytona"
	newUid := currentUser.Uid
	newGid := currentUser.Gid

	if opts.SshClient != nil {
		newUid, newGid, err = opts.SshClient.GetUserUidGid()
		if err != nil {
			return "", err
		}
	}

	if newUid == "0" && newGid == "0" {
		containerUser = "root"
	}

	/*
		Patch UID and GID of the user cloning the repository
	*/
	if containerUser != "root" {
		_, err = d.ExecSync(containerId, container.ExecOptions{
			User: "root",
			Cmd:  []string{"sh", "-c", UPDATE_UID_GID_SCRIPT},
			Env: []string{
				fmt.Sprintf("REMOTE_USER=%s", containerUser),
				fmt.Sprintf("NEW_UID=%s", newUid),
				fmt.Sprintf("NEW_GID=%s", newGid),
			},
		}, opts.LogWriter)
		if err != nil {
			return "", err
		}
	}

	return containerUser, nil
}

func (d *DockerClient) toCreateDevcontainerOptions(opts *CreateWorkspaceOptions, prebuild bool) CreateDevcontainerOptions {
	return CreateDevcontainerOptions{
		WorkspaceDir:        opts.WorkspaceDir,
		WorkspaceFolderName: opts.Workspace.WorkspaceFolderName(),
		BuildConfig:         opts.Workspace.BuildConfig,
		LogWriter:           opts.LogWriter,
		SshClient:           opts.SshClient,
		ContainerRegistries: opts.ContainerRegistries,
		BuilderImage:        opts.BuilderImage,
		EnvVars:             opts.Workspace.EnvVars,
		IdLabels: map[string]string{
			"daytona.target.id":    opts.Workspace.TargetId,
			"daytona.workspace.id": opts.Workspace.Id,
		},
		Prebuild: prebuild,
	}
}

const UPDATE_UID_GID_SCRIPT = `eval $(sed -n "s/${REMOTE_USER}:[^:]*:\([^:]*\):\([^:]*\):[^:]*:\([^:]*\).*/OLD_UID=\1;OLD_GID=\2;HOME_FOLDER=\3/p" /etc/passwd); \
eval $(sed -n "s/\([^:]*\):[^:]*:${NEW_UID}:.*/EXISTING_USER=\1/p" /etc/passwd); \
eval $(sed -n "s/\([^:]*\):[^:]*:${NEW_GID}:.*/EXISTING_GROUP=\1/p" /etc/group); \
if [ -z "$OLD_UID" ]; then \
	echo "Remote user not found in /etc/passwd ($REMOTE_USER)."; \
elif [ "$OLD_UID" = "$NEW_UID" -a "$OLD_GID" = "$NEW_GID" ]; then \
	echo "UIDs and GIDs are the same ($NEW_UID:$NEW_GID)."; \
elif [ "$OLD_UID" != "$NEW_UID" -a -n "$EXISTING_USER" ]; then \
	echo "User with UID exists ($EXISTING_USER=$NEW_UID)."; \
else \
	if [ "$OLD_GID" != "$NEW_GID" -a -n "$EXISTING_GROUP" ]; then \
		echo "Group with GID exists ($EXISTING_GROUP=$NEW_GID)."; \
		NEW_GID="$OLD_GID"; \
	fi; \
	echo "Updating UID:GID from $OLD_UID:$OLD_GID to $NEW_UID:$NEW_GID."; \
	sed -i -e "s/\(${REMOTE_USER}:[^:]*:\)[^:]*:[^:]*/\1${NEW_UID}:${NEW_GID}/" /etc/passwd; \
	if [ "$OLD_GID" != "$NEW_GID" ]; then \
		sed -i -e "s/\([^:]*:[^:]*:\)${OLD_GID}:/\1${NEW_GID}:/" /etc/group; \
	fi; \
	chown -R $NEW_UID:$NEW_GID $HOME_FOLDER; \
fi;`
