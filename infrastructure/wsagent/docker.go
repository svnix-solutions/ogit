package wsagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/labbs/ogit/application"
)

const (
	labelWorkspace = "ogit.workspace"
	labelDiskGB    = "ogit.disk-gb"
	workspaceUser  = "1000:1000"
	homeDir        = "/home/coder"
	cloneDir       = homeDir + "/workspace"
	tokenDir       = "/run/ogit"
	provisionLimit = 10 * time.Minute
	gb             = int64(1) << 30
)

// provision stores the token on a tmpfs, for git to read as a credential
// helper, and clones on first start. The token comes through the exec's
// environment, never an argument, so it's in no process list or file on disk.
const provision = `set -eu
umask 077
printf %s "$OGIT_TOKEN" > ` + tokenDir + `/token
unset OGIT_TOKEN
git config --global credential.helper '!f() { test "$1" = get && printf "username=workspace\npassword=%s\n" "$(cat ` + tokenDir + `/token)"; }; f'
if [ ! -d "$OGIT_DIR/.git" ]; then
	git clone --branch "$OGIT_BRANCH" -- "$OGIT_URL" "$OGIT_DIR"
fi
`

// CodeServerCommand runs the image's editor, listening on the container's
// own network only: no port is published.
var CodeServerCommand = []string{"--auth", "none", "--bind-addr", "0.0.0.0:8080", "--disable-telemetry", "--disable-update-check", "--disable-workspace-trust", cloneDir}

// DockerRuntime runs workspaces on a Docker daemon.
type DockerRuntime struct {
	Docker *client.Client
	// GitURL is where containers reach the server.
	GitURL string
	// Network for containers (empty: Docker's default).
	Network string
	// Command overrides CodeServerCommand.
	Command []string
}

// NewDockerRuntime connects to the daemon at host (empty: DOCKER_HOST or the
// usual socket).
func NewDockerRuntime(host, gitURL, network string) (*DockerRuntime, error) {
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}
	docker, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, err
	}
	return &DockerRuntime{Docker: docker, GitURL: strings.TrimSuffix(gitURL, "/"), Network: network, Command: CodeServerCommand}, nil
}

func containerName(id string) string { return "ogit-workspace-" + id }
func volumeName(id string) string    { return "ogit-workspace-" + id }

func (d *DockerRuntime) Running(ctx context.Context) ([]string, error) {
	list, err := d.Docker.ContainerList(ctx, container.ListOptions{Filters: filters.NewArgs(filters.Arg("label", labelWorkspace), filters.Arg("status", "running"))})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(list))
	for _, c := range list {
		ids = append(ids, c.Labels[labelWorkspace])
	}
	return ids, nil
}

func (d *DockerRuntime) pull(ctx context.Context, ref string) error {
	if _, err := d.Docker.ImageInspect(ctx, ref); err == nil {
		return nil
	}
	r, err := d.Docker.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer r.Close()
	_, err = io.Copy(io.Discard, r)
	return err
}

// hostConfig is what keeps a workspace in its place: no capabilities, no new
// privileges, a read-only root (writes go to the volume and small tmpfs
// mounts, so only the volume can grow), limits, and no published port.
func (d *DockerRuntime) hostConfig(task application.WorkspaceTask) *container.HostConfig {
	pids := int64(task.Pids)
	memory := int64(task.MemoryGB) * gb
	return &container.HostConfig{
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		ReadonlyRootfs: true,
		Tmpfs: map[string]string{
			"/tmp":   "rw,nosuid,nodev,size=1g,mode=1777",
			tokenDir: "rw,nosuid,nodev,noexec,size=1m,mode=0700,uid=1000,gid=1000",
		},
		Mounts:      []mount.Mount{{Type: mount.TypeVolume, Source: volumeName(task.Id), Target: homeDir}},
		NetworkMode: container.NetworkMode(d.Network),
		Resources: container.Resources{
			NanoCPUs: int64(task.CPUs) * 1_000_000_000,
			Memory:   memory, MemorySwap: memory, // no swap on top
			PidsLimit: &pids,
		},
	}
}

func (d *DockerRuntime) Start(ctx context.Context, task application.WorkspaceTask) error {
	if !strings.Contains(task.Image, "@sha256:") {
		return errors.New("the workspace image isn't pinned by digest")
	}
	if err := d.pull(ctx, task.Image); err != nil {
		return fmt.Errorf("pulling the image: %w", err)
	}
	labels := map[string]string{labelWorkspace: task.Id, labelDiskGB: strconv.Itoa(task.DiskGB)}
	if _, err := d.Docker.VolumeCreate(ctx, volume.CreateOptions{Name: volumeName(task.Id), Labels: labels}); err != nil {
		return fmt.Errorf("creating the volume: %w", err)
	}
	if err := d.Stop(ctx, task.Id); err != nil {
		return err
	}
	created, err := d.Docker.ContainerCreate(ctx, &container.Config{Image: task.Image, User: workspaceUser, Cmd: d.Command, Labels: labels},
		d.hostConfig(task), nil, nil, containerName(task.Id))
	if err != nil {
		return fmt.Errorf("creating the container: %w", err)
	}
	err = d.Docker.ContainerStart(ctx, created.ID, container.StartOptions{})
	if err == nil {
		err = d.provision(ctx, created.ID, task)
	}
	if err != nil {
		_ = d.Stop(ctx, task.Id)
		return err
	}
	return nil
}

func (d *DockerRuntime) provision(ctx context.Context, id string, task application.WorkspaceTask) error {
	ctx, cancel := context.WithTimeout(ctx, provisionLimit)
	defer cancel()
	url := d.GitURL + "/" + strings.TrimPrefix(task.Repository, "/")
	exec, err := d.Docker.ContainerExecCreate(ctx, id, container.ExecOptions{
		User: workspaceUser, Cmd: []string{"sh", "-c", provision}, AttachStdout: true, AttachStderr: true,
		Env: []string{"OGIT_TOKEN=" + task.Token, "OGIT_BRANCH=" + task.Branch, "OGIT_URL=" + url, "OGIT_DIR=" + cloneDir, "HOME=" + homeDir},
	})
	if err != nil {
		return err
	}
	attach, err := d.Docker.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{})
	if err != nil {
		return err
	}
	defer attach.Close()
	var output bytes.Buffer
	if _, err := stdcopy.StdCopy(&output, &output, attach.Reader); err != nil {
		return err
	}
	result, err := d.Docker.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("cloning failed: %s", strings.TrimSpace(output.String()))
	}
	return nil
}

func (d *DockerRuntime) Stop(ctx context.Context, id string) error {
	err := d.Docker.ContainerRemove(ctx, containerName(id), container.RemoveOptions{Force: true})
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}

func (d *DockerRuntime) Remove(ctx context.Context, id string) error {
	if err := d.Stop(ctx, id); err != nil {
		return err
	}
	err := d.Docker.VolumeRemove(ctx, volumeName(id), true)
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}

// OverDisk asks Docker how big volumes are: it can't limit them on its usual
// storage, so a workspace over its limit is stopped instead.
func (d *DockerRuntime) OverDisk(ctx context.Context) ([]string, error) {
	usage, err := d.Docker.DiskUsage(ctx, types.DiskUsageOptions{Types: []types.DiskUsageObject{types.VolumeObject}})
	if err != nil {
		return nil, err
	}
	var over []string
	for _, v := range usage.Volumes {
		id, limit := v.Labels[labelWorkspace], v.Labels[labelDiskGB]
		diskGB, err := strconv.Atoi(limit)
		if id == "" || err != nil || diskGB <= 0 || v.UsageData == nil {
			continue
		}
		if v.UsageData.Size > int64(diskGB)*gb {
			over = append(over, id)
		}
	}
	return over, nil
}
