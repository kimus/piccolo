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
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
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
	Source  string // host PicoClaw home dir (absolute), mounted to MountTarget
	Ports   []string
	Env     []string // KEY=value pairs, already resolved
	Volumes []string // docker-style bind mounts, e.g. /host:/path:rw or name:/path:rw
	Labels  map[string]string
	// Entrypoint and Command override the image defaults for one-shot helper
	// containers. Leave empty for the PicoClaw gateway's default entrypoint.
	Entrypoint []string
	Command    []string
	TTY        bool
	// Detach runs the container in the background without stdio attachment
	// (logs remain available through the daemon).
	Detach bool
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

// ImageBuildOptions describes a daemon-side image build without exposing SDK
// types to callers.
type ImageBuildOptions struct {
	Tag        string
	Dockerfile string
	Pull       bool
	Remove     bool
}

// BuildImage sends a tar build context to the daemon and streams the daemon's
// build output to out. Callers own the context reader and may generate it in
// memory or from a temporary directory.
func (r *Runner) BuildImage(ctx context.Context, buildContext io.Reader, opts ImageBuildOptions, out io.Writer) error {
	if strings.TrimSpace(opts.Tag) == "" {
		return errors.New("image tag is required")
	}
	if strings.TrimSpace(opts.Dockerfile) == "" {
		opts.Dockerfile = "Dockerfile"
	}
	cli, err := r.newClient(ctx)
	if err != nil {
		return err
	}
	res, err := cli.ImageBuild(ctx, buildContext, mobyclient.ImageBuildOptions{
		Tags:       []string{opts.Tag},
		Dockerfile: opts.Dockerfile,
		PullParent: opts.Pull,
		Remove:     opts.Remove,
	})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if out == nil {
		_, err = io.Copy(io.Discard, res.Body)
		return err
	}
	return writeBuildOutput(res.Body, out)
}

func writeBuildOutput(r io.Reader, out io.Writer) error {
	s := bufio.NewScanner(r)
	// Build lines can include long progress entries; allow more than Scanner's
	// default 64 KiB token limit while keeping memory bounded.
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for s.Scan() {
		line := s.Bytes()
		var msg struct {
			Stream      string `json:"stream"`
			Status      string `json:"status"`
			ID          string `json:"id"`
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			fmt.Fprintln(out, string(line))
			continue
		}
		if msg.Error != "" || msg.ErrorDetail.Message != "" {
			if msg.ErrorDetail.Message != "" {
				fmt.Fprintln(out, msg.ErrorDetail.Message)
			} else {
				fmt.Fprintln(out, msg.Error)
			}
			continue
		}
		if msg.Stream != "" {
			fmt.Fprint(out, msg.Stream)
			continue
		}
		if msg.Status != "" {
			if msg.ID != "" {
				fmt.Fprintf(out, "%s: %s\n", msg.ID, msg.Status)
			} else {
				fmt.Fprintln(out, msg.Status)
			}
		}
	}
	return s.Err()
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
	attach := !spec.Detach
	config := &mobycontainer.Config{
		Image:        spec.Image,
		Env:          spec.Env,
		Labels:       labels,
		Entrypoint:   spec.Entrypoint,
		Cmd:          spec.Command,
		OpenStdin:    attach,
		StdinOnce:    attach,
		AttachStdin:  attach,
		AttachStdout: attach,
		AttachStderr: attach,
		Tty:          spec.TTY,
		ExposedPorts: exposedPorts,
	}
	binds := []string{fmt.Sprintf("%s:%s:rw", spec.Source, MountTarget)}
	binds = append(binds, spec.Volumes...)
	hostConfig := &mobycontainer.HostConfig{
		Binds:        binds,
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
	proc := newContainerProcess(cli, id, attach, prepared.Config.Tty)
	if _, err := cli.ContainerStart(ctx, id, mobyclient.ContainerStartOptions{}); err != nil {
		_ = proc.Shutdown(0)
		return nil, fmt.Errorf("start container: %w", err)
	}
	return proc, nil
}

// Run prepares and starts the spec in one step (attached, foreground).
func (r *Runner) Run(ctx context.Context, spec Spec) (Process, error) {
	prepared, err := r.Prepare(spec)
	if err != nil {
		return nil, err
	}
	return r.Start(ctx, prepared)
}

// StartDetached creates and starts the container in the background, returning
// its id. The container keeps running after piccolo exits.
func (r *Runner) StartDetached(ctx context.Context, spec Spec) (string, error) {
	spec.Detach = true
	prepared, err := r.Prepare(spec)
	if err != nil {
		return "", err
	}
	cli, err := r.newClient(ctx)
	if err != nil {
		return "", fmt.Errorf("connect to container daemon: %w", err)
	}
	if prepared.Name != "" {
		removeStaleByName(ctx, cli, prepared.Name)
	}
	id, err := createContainer(ctx, cli, prepared)
	if err != nil {
		return "", err
	}
	if _, err := cli.ContainerStart(ctx, id, mobyclient.ContainerStartOptions{}); err != nil {
		stopRemove(ctx, cli, id)
		return "", fmt.Errorf("start container: %w", err)
	}
	return id, nil
}

// State describes a container's current lifecycle status.
type State struct {
	Found    bool
	Running  bool
	Status   string
	ExitCode int
}

// StateByName inspects a container by name.
func (r *Runner) StateByName(ctx context.Context, name string) (State, error) {
	cli, err := r.newClient(ctx)
	if err != nil {
		return State{}, err
	}
	found, err := cli.FindContainerByName(ctx, name)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return State{}, nil
		}
		return State{}, err
	}
	if found == nil {
		return State{}, nil
	}
	res, err := cli.ContainerInspect(ctx, found.ID, mobyclient.ContainerInspectOptions{})
	if err != nil {
		return State{}, err
	}
	st := State{Found: true}
	if res.Container.State != nil {
		st.Status = string(res.Container.State.Status)
		st.Running = res.Container.State.Running
		st.ExitCode = res.Container.State.ExitCode
	}
	return st, nil
}

// StopByName gracefully stops and removes a container by name. It reports
// whether a container was found.
func (r *Runner) StopByName(ctx context.Context, name string, grace time.Duration) (bool, error) {
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
	if found == nil {
		return false, nil
	}
	if grace <= 0 {
		grace = DefaultShutdownGrace
	}
	timeout := int(grace / time.Second)
	if timeout < 1 {
		timeout = 1
	}
	if _, err := cli.ContainerStop(ctx, found.ID, mobyclient.ContainerStopOptions{Timeout: &timeout}); err != nil && !errdefs.IsNotFound(err) {
		return true, fmt.Errorf("stop container: %w", err)
	}
	if _, err := cli.ContainerRemove(ctx, found.ID, mobyclient.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return true, fmt.Errorf("remove container: %w", err)
	}
	return true, nil
}

// Logs streams container logs. The returned reader carries docker stream
// framing (use Demux to split stdout/stderr). The caller closes it.
func (r *Runner) Logs(ctx context.Context, name string, follow bool, tail string) (io.ReadCloser, error) {
	cli, err := r.newClient(ctx)
	if err != nil {
		return nil, err
	}
	found, err := cli.FindContainerByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("container %q not found", name)
	}
	res, err := cli.ContainerLogs(ctx, found.ID, mobyclient.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tail,
	})
	if err != nil {
		return nil, fmt.Errorf("fetch logs: %w", err)
	}
	return res, nil
}

// ExecSession is an attached exec instance. With tty=false the Reader carries
// docker stream framing (use Demux); with tty=true it is a raw byte stream.
type ExecSession struct {
	conn   net.Conn
	hijack mobyclient.HijackedResponse
	Reader io.Reader
	execID string
	cli    client.SDKClient
}

// Stdin writes to the exec process; Close half-closes it (EOF).
func (s *ExecSession) Stdin() io.WriteCloser {
	return attachStdin{conn: s.conn, hijack: s.hijack}
}

// Close tears down the hijacked connection.
func (s *ExecSession) Close() error {
	return s.conn.Close()
}

// Wait blocks until the exec process finishes and returns its exit code.
func (s *ExecSession) Wait(ctx context.Context) (int, error) {
	for {
		res, err := s.cli.ExecInspect(ctx, s.execID, mobyclient.ExecInspectOptions{})
		if err != nil {
			return -1, err
		}
		if !res.Running {
			return res.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// Exec runs a command inside the named container and attaches its streams.
// RemoveVolume removes a named Docker volume.
func (r *Runner) RemoveVolume(ctx context.Context, name string, force bool) error {
	cli, err := r.newClient(ctx)
	if err != nil {
		return err
	}
	_, err = cli.VolumeRemove(ctx, name, mobyclient.VolumeRemoveOptions{Force: force})
	return err
}

func (r *Runner) Exec(ctx context.Context, name string, cmd []string, tty bool) (*ExecSession, error) {
	cli, err := r.newClient(ctx)
	if err != nil {
		return nil, err
	}
	found, err := cli.FindContainerByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("container %q not found — is the gateway running? (piccolo serve)", name)
	}
	created, err := cli.ExecCreate(ctx, found.ID, mobyclient.ExecCreateOptions{
		Cmd:          cmd,
		TTY:          tty,
		AttachStdin:  tty,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create exec: %w", err)
	}
	attached, err := cli.ExecAttach(ctx, created.ID, mobyclient.ExecAttachOptions{TTY: tty})
	if err != nil {
		return nil, fmt.Errorf("attach exec: %w", err)
	}
	return &ExecSession{
		conn:   attached.Conn,
		hijack: attached.HijackedResponse,
		Reader: attached.Reader,
		execID: created.ID,
		cli:    cli,
	}, nil
}

// Demux splits a multiplexed (non-tty) docker stream into stdout and stderr.
func Demux(r io.Reader, stdout, stderr io.Writer) error {
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		out := stdout
		if header[0] == 2 {
			out = stderr
		}
		if _, err := io.CopyN(out, r, size); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
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

func newContainerProcess(cli client.SDKClient, id string, attach mobyclient.ContainerAttachResult, tty bool) *containerProcess {
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
	go p.pump(attach.Reader, stdoutW, stderrW, tty)
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
func (p *containerProcess) pump(r io.Reader, stdoutW, stderrW *io.PipeWriter, tty bool) {
	defer func() {
		_ = stdoutW.Close()
		_ = stderrW.Close()
		close(p.demuxDone)
	}()
	if tty {
		_, _ = io.Copy(stdoutW, r)
		return
	}
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
