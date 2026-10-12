// Package docker runs the PicoClaw gateway container through the container
// engine API (github.com/docker/go-sdk), removing the docker CLI as a hard
// dependency. SDK types never leave this package: the rest of the codebase
// sees only Runner, Process, and Spec.
//
// Ported and slimmed from piccolo-full internal/isolation/dockerapi. The
// go-sdk is self-described WIP until v1.0.0; module versions are pinned
// exactly in go.mod — never float them.
package docker

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/go-sdk/client"
	mobycontainer "github.com/moby/moby/api/types/container"
	mobynetwork "github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"
)

// Labels attached to every container Piccolo creates.
const (
	LabelManaged   = "piccolo.managed"
	LabelWorkspace = "piccolo.workspace"
)

// DefaultShutdownGrace is the wait for a natural exit before forcing stop.
const DefaultShutdownGrace = 10 * time.Second

// MountTarget is the in-container PicoClaw home (its default location, so
// PICOCLAW_HOME never needs to be set).
const MountTarget = "/root/.picoclaw"

// Spec is the launch plan for the gateway container, composed from
// piccolo.yaml plus the resolved workspace path.
type Spec struct {
	Name    string
	Image   string
	Network string // none | bridge | host ("" = bridge)
	Source  string // host workspace dir (absolute), mounted to MountTarget
	Ports   []string
	Env     []string // KEY=value pairs, already resolved
	Labels  map[string]string
}

// Process is a running container with its PID1 stdio attached.
type Process interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	// Wait blocks until the container exits naturally and returns its exit
	// code. It returns early with an error if ctx is cancelled.
	Wait(ctx context.Context) (int, error)
	// Shutdown gracefully stops and removes the container, returning only
	// after cleanup. ExitCode is valid afterwards.
	Shutdown(grace time.Duration) error
	ExitCode() int
}

// Runner manages container lifecycle through the container engine API.
type Runner struct {
	// ProbeTimeout bounds the daemon reachability check. Default 3s.
	ProbeTimeout time.Duration

	newClient func(ctx context.Context) (client.SDKClient, error)
}

// New creates a runner talking to whatever daemon the SDK resolves from the
// current docker context (socket, rootless, remote).
func New() *Runner {
	return &Runner{
		ProbeTimeout: 3 * time.Second,
		newClient: func(ctx context.Context) (client.SDKClient, error) {
			return client.New(ctx)
		},
	}
}

// Ping reports whether the daemon is reachable.
func (r *Runner) Ping(ctx context.Context) error {
	cli, err := r.newClient(ctx)
	if err != nil {
		return err
	}
	_, err = cli.Ping(ctx, mobyclient.PingOptions{})
	return err
}

// Available reports whether the daemon answers a ping within ProbeTimeout.
func (r *Runner) Available() bool {
	timeout := r.ProbeTimeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return r.Ping(ctx) == nil
}

// ImageExists reports whether the image is present locally.
func (r *Runner) ImageExists(ctx context.Context, image string) (bool, error) {
	cli, err := r.newClient(ctx)
	if err != nil {
		return false, err
	}
	_, err = cli.ImageInspect(ctx, image)
	if err == nil {
		return true, nil
	}
	if errdefs.IsNotFound(err) || strings.Contains(err.Error(), "No such image") {
		return false, nil
	}
	return false, err
}

// ContainerExists reports whether a container with the given name exists
// (running or stopped).
func (r *Runner) ContainerExists(ctx context.Context, name string) (bool, error) {
	cli, err := r.newClient(ctx)
	if err != nil {
		return false, err
	}
	found, err := cli.FindContainerByName(ctx, name)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return found != nil, nil
}

// preparedContainer is the launch plan without any daemon interaction yet.
type preparedContainer struct {
	Name       string
	Image      string
	Config     *mobycontainer.Config
	HostConfig *mobycontainer.HostConfig
}

// Prepare validates the spec and composes the container configuration. It
// performs no daemon calls, so a failure leaves no partial state.
func (r *Runner) Prepare(spec Spec) (*preparedContainer, error) {
	if strings.TrimSpace(spec.Image) == "" {
		return nil, errors.New("container image is required")
	}
	if strings.TrimSpace(spec.Source) == "" {
		return nil, errors.New("workspace source path is required")
	}

	networkMode := mobycontainer.NetworkMode("")
	switch spec.Network {
	case "", "bridge":
	case "none":
		networkMode = mobycontainer.NetworkMode("none")
	case "host":
		networkMode = mobycontainer.NetworkMode("host")
	default:
		return nil, fmt.Errorf("unsupported network mode %q", spec.Network)
	}

	exposedPorts := mobynetwork.PortSet{}
	portBindings := mobynetwork.PortMap{}
	for _, port := range spec.Ports {
		if strings.TrimSpace(port) == "" {
			continue
		}
		containerPort, binding, err := parsePortSpec(port)
		if err != nil {
			return nil, err
		}
		exposedPorts[containerPort] = struct{}{}
		portBindings[containerPort] = append(portBindings[containerPort], binding)
	}

	labels := map[string]string{LabelManaged: "true"}
	for k, v := range spec.Labels {
		labels[k] = v
	}

	init := true
	config := &mobycontainer.Config{
		Image:        spec.Image,
		Env:          spec.Env,
		Labels:       labels,
		OpenStdin:    true,
		StdinOnce:    true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
		ExposedPorts: exposedPorts,
	}
	hostConfig := &mobycontainer.HostConfig{
		Binds:        []string{fmt.Sprintf("%s:%s:rw", spec.Source, MountTarget)},
		NetworkMode:  networkMode,
		PortBindings: portBindings,
		Init:         &init,
		// Explicit termination on shutdown replaces --rm; crashed runs are
		// covered by stale-name removal on the next start.
		AutoRemove: false,
	}
	return &preparedContainer{
		Name:       strings.TrimSpace(spec.Name),
		Image:      spec.Image,
		Config:     config,
		HostConfig: hostConfig,
	}, nil
}

// Start creates the container (pulling a missing image once), attaches PID1
// stdio, and starts it. Attach-before-start avoids losing stderr from very
// fast failures. Any failure after creation cleans the container back up.
func (r *Runner) Start(ctx context.Context, prepared *preparedContainer) (Process, error) {
	cli, err := r.newClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to container daemon: %w", err)
	}

	// A stale container carrying the configured name must not block restart.
	if prepared.Name != "" {
		removeStaleByName(ctx, cli, prepared.Name)
	}

	id, err := createContainer(ctx, cli, prepared)
	if err != nil {
		return nil, err
	}
	attach, err := cli.ContainerAttach(ctx, id, mobyclient.ContainerAttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		stopRemove(ctx, cli, id)
		return nil, fmt.Errorf("attach container stdio: %w", err)
	}
	proc := newContainerProcess(cli, id, attach)
	if _, err := cli.ContainerStart(ctx, id, mobyclient.ContainerStartOptions{}); err != nil {
		_ = proc.Shutdown(0)
		return nil, fmt.Errorf("start container: %w", err)
	}
	return proc, nil
}

// Run prepares and starts the spec in one step.
func (r *Runner) Run(ctx context.Context, spec Spec) (Process, error) {
	prepared, err := r.Prepare(spec)
	if err != nil {
		return nil, err
	}
	return r.Start(ctx, prepared)
}

func createContainer(ctx context.Context, cli client.SDKClient, pc *preparedContainer) (string, error) {
	options := mobyclient.ContainerCreateOptions{
		Name:       pc.Name,
		Config:     pc.Config,
		HostConfig: pc.HostConfig,
	}
	res, err := cli.ContainerCreate(ctx, options)
	if err != nil && isMissingImage(err) {
		if pullErr := pullImage(ctx, cli, pc.Image); pullErr != nil {
			return "", pullErr
		}
		res, err = cli.ContainerCreate(ctx, options)
	}
	if err != nil {
		return "", fmt.Errorf("create container for image %q: %w", pc.Image, err)
	}
	return res.ID, nil
}

func isMissingImage(err error) bool {
	return errdefs.IsNotFound(err) || strings.Contains(err.Error(), "No such image")
}

func pullImage(ctx context.Context, cli client.SDKClient, image string) error {
	resp, err := cli.ImagePull(ctx, image, mobyclient.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull image %q: %w", image, err)
	}
	defer resp.Close()
	if err := resp.Wait(ctx); err != nil {
		return fmt.Errorf("pull image %q: %w", image, err)
	}
	return nil
}

func removeStaleByName(ctx context.Context, cli client.SDKClient, name string) {
	found, err := cli.FindContainerByName(ctx, name)
	if err != nil || found == nil {
		return
	}
	stopRemove(ctx, cli, found.ID)
}

func stopRemove(ctx context.Context, cli client.SDKClient, id string) {
	zero := 0
	_, _ = cli.ContainerStop(ctx, id, mobyclient.ContainerStopOptions{Timeout: &zero})
	_, _ = cli.ContainerRemove(ctx, id, mobyclient.ContainerRemoveOptions{Force: true})
}

// containerProcess is a started container with its PID1 stdio attached.
// Stdout and stderr arrive multiplexed over the hijacked connection and are
// demultiplexed into separate pipes.
type containerProcess struct {
	cli  client.SDKClient
	id   string
	conn net.Conn

	stdin   io.WriteCloser
	stdoutR *io.PipeReader
	stderrR *io.PipeReader

	demuxDone chan struct{}

	mu       sync.Mutex
	exitCode int
}

func newContainerProcess(cli client.SDKClient, id string, attach mobyclient.ContainerAttachResult) *containerProcess {
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	p := &containerProcess{
		cli:       cli,
		id:        id,
		conn:      attach.Conn,
		stdin:     attachStdin{conn: attach.Conn, hijack: attach.HijackedResponse},
		stdoutR:   stdoutR,
		stderrR:   stderrR,
		demuxDone: make(chan struct{}),
		exitCode:  -1,
	}
	go p.pump(attach.Reader, stdoutW, stderrW)
	return p
}

// attachStdin writes to the hijacked connection; Close half-closes it so the
// container observes EOF on stdin while output streams keep flowing.
type attachStdin struct {
	conn   net.Conn
	hijack mobyclient.HijackedResponse
}

func (s attachStdin) Write(p []byte) (int, error) { return s.conn.Write(p) }
func (s attachStdin) Close() error                { return s.hijack.CloseWrite() }

// pump demultiplexes the docker stream framing:
// [8]byte{STREAM_TYPE, 0, 0, 0, SIZE1..SIZE4} followed by SIZE bytes.
func (p *containerProcess) pump(r io.Reader, stdoutW, stderrW *io.PipeWriter) {
	defer func() {
		_ = stdoutW.Close()
		_ = stderrW.Close()
		close(p.demuxDone)
	}()
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			return
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		out := stdoutW
		if header[0] == 2 {
			out = stderrW
		}
		if _, err := io.CopyN(out, r, size); err != nil {
			return
		}
	}
}

func (p *containerProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *containerProcess) Stdout() io.ReadCloser { return p.stdoutR }
func (p *containerProcess) Stderr() io.ReadCloser { return p.stderrR }

// Wait polls container state until it exits naturally.
func (p *containerProcess) Wait(ctx context.Context) (int, error) {
	for {
		running, code, err := p.inspectState(ctx)
		if err != nil {
			return -1, err
		}
		if !running {
			p.setExitCode(code)
			return code, nil
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (p *containerProcess) ExitCode() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitCode
}

func (p *containerProcess) setExitCode(code int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exitCode = code
}

// Shutdown stops and removes the container, returning only after cleanup.
// Unlike a job runner, a foreground gateway will not exit on stdin EOF, so
// this proceeds directly to a graceful ContainerStop (SIGTERM to PID1, then
// SIGKILL after grace) instead of waiting for a natural exit.
func (p *containerProcess) Shutdown(grace time.Duration) error {
	if grace <= 0 {
		grace = DefaultShutdownGrace
	}
	ctx, cancel := context.WithTimeout(context.Background(), grace+15*time.Second)
	defer cancel()

	_ = p.stdin.Close()

	// If the container already exited on its own, capture its status.
	running, code, err := p.inspectState(ctx)
	if err == nil && !running {
		p.setExitCode(code)
	} else {
		timeout := int(grace / time.Second)
		if timeout < 1 {
			timeout = 1
		}
		_, _ = p.cli.ContainerStop(ctx, p.id, mobyclient.ContainerStopOptions{Timeout: &timeout})
		if running, code, err := p.inspectState(ctx); err == nil && !running {
			p.setExitCode(code)
		}
	}

	_, removeErr := p.cli.ContainerRemove(ctx, p.id, mobyclient.ContainerRemoveOptions{Force: true})
	if removeErr != nil && errdefs.IsNotFound(removeErr) {
		removeErr = nil
	}

	p.conn.Close()
	select {
	case <-p.demuxDone:
	case <-time.After(2 * time.Second):
	}

	if removeErr != nil {
		return fmt.Errorf("remove container %s: %w", shortID(p.id), removeErr)
	}
	return nil
}

// inspectState reports whether the container still runs and, when it has
// exited, its exit code.
func (p *containerProcess) inspectState(ctx context.Context) (bool, int, error) {
	res, err := p.cli.ContainerInspect(ctx, p.id, mobyclient.ContainerInspectOptions{})
	if err != nil {
		return false, -1, err
	}
	if res.Container.State == nil {
		return false, -1, nil
	}
	return res.Container.State.Running, res.Container.State.ExitCode, nil
}

// parsePortSpec parses CLI-style port declarations ("host_ip:host:container",
// "host:container", "host:container/proto", "container") into API types.
func parsePortSpec(spec string) (mobynetwork.Port, mobynetwork.PortBinding, error) {
	proto := "tcp"
	portion := strings.TrimSpace(spec)
	if i := strings.LastIndex(portion, "/"); i >= 0 {
		proto = portion[i+1:]
		portion = portion[:i]
	}
	parts := strings.Split(portion, ":")
	var hostIP, hostPort, containerPort string
	switch len(parts) {
	case 1:
		containerPort = parts[0]
	case 2:
		hostPort, containerPort = parts[0], parts[1]
	case 3:
		hostIP, hostPort, containerPort = parts[0], parts[1], parts[2]
	default:
		return mobynetwork.Port{}, mobynetwork.PortBinding{}, fmt.Errorf("unsupported port specification %q (want host:container)", spec)
	}
	num, err := strconv.Atoi(strings.TrimSpace(containerPort))
	if err != nil || num <= 0 || num > 65535 {
		return mobynetwork.Port{}, mobynetwork.PortBinding{}, fmt.Errorf("unsupported port specification %q (invalid container port)", spec)
	}
	port, err := mobynetwork.ParsePort(fmt.Sprintf("%d/%s", num, proto))
	if err != nil {
		return mobynetwork.Port{}, mobynetwork.PortBinding{}, fmt.Errorf("unsupported port specification %q: %w", spec, err)
	}
	var hostAddr netip.Addr
	if hostIP = strings.TrimSpace(hostIP); hostIP != "" {
		hostAddr, err = netip.ParseAddr(hostIP)
		if err != nil {
			return mobynetwork.Port{}, mobynetwork.PortBinding{}, fmt.Errorf("unsupported port specification %q (invalid host IP): %w", spec, err)
		}
	}
	return port, mobynetwork.PortBinding{HostIP: hostAddr, HostPort: strings.TrimSpace(hostPort)}, nil
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
