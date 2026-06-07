package controllerproc

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gemyago/oke-gateway-api/e2e/internal/config"
)

const (
	envKubeconfig          = "KUBECONFIG"
	envOCIConfigFile       = "OCI_CONFIG_FILE"
	envOCIConfigFileAlt    = "OCI_CLI_CONFIG_FILE"
	envOCIConfigProfile    = "OCI_CLI_PROFILE"
	envOCIConfigProfileAlt = "OCI_CLI_CONFIG_PROFILE"
	envControllerBin       = "OKE_E2E_CONTROLLER_BIN"
	envSkipController      = "OKE_E2E_SKIP_CONTROLLER_START"
	envK8sAPINoop          = "APP_K8SAPI_NOOP"
	envOCIAPINoop          = "APP_OCIAPI_NOOP"
)

const (
	defaultStopTimeout      = 10 * time.Second
	controllerStreamCount   = 2
	streamScannerBufferSize = 64 * 1024
	streamScannerMaxToken   = 1024 * 1024
)

type TestLogSink interface {
	Helper()
	Cleanup(func())
	Logf(string, ...any)
	Errorf(string, ...any)
}

type StartOptions struct {
	command     func(string, ...string) *exec.Cmd
	environ     []string
	stat        func(string) (fs.FileInfo, error)
	stopTimeout time.Duration
}

type Process struct {
	path        string
	pid         int
	skipped     bool
	logf        func(string, ...any)
	stopTimeout time.Duration
	cmd         *exec.Cmd
	waitDone    chan error
	streamsDone chan struct{}
	stopOnce    sync.Once
	stopErr     error
}

func NewStartOptions() *StartOptions {
	return &StartOptions{
		command:     exec.Command,
		stat:        os.Stat,
		stopTimeout: defaultStopTimeout,
	}
}

func (opts *StartOptions) WithCommand(fn func(string, ...string) *exec.Cmd) *StartOptions {
	opts.command = fn
	return opts
}

func (opts *StartOptions) WithEnviron(env []string) *StartOptions {
	opts.environ = append([]string(nil), env...)
	return opts
}

func (opts *StartOptions) WithStat(fn func(string) (fs.FileInfo, error)) *StartOptions {
	opts.stat = fn
	return opts
}

func (opts *StartOptions) WithStopTimeout(timeout time.Duration) *StartOptions {
	opts.stopTimeout = timeout
	return opts
}

func Start(t TestLogSink, cfg config.Config, opts *StartOptions) (*Process, error) {
	t.Helper()

	if opts == nil {
		opts = NewStartOptions()
	}

	if opts.command == nil {
		opts.command = exec.Command
	}

	if opts.stat == nil {
		opts.stat = os.Stat
	}

	if opts.stopTimeout <= 0 {
		opts.stopTimeout = defaultStopTimeout
	}

	if cfg.Controller.SkipStart {
		t.Logf("controller start skipped because %s=true", envSkipController)
		return &Process{
			path:        cfg.Controller.BinPath,
			skipped:     true,
			logf:        t.Logf,
			stopTimeout: opts.stopTimeout,
			waitDone:    closedWaitChannel(nil),
			streamsDone: closedSignalChannel(),
		}, nil
	}

	if strings.TrimSpace(cfg.Kubernetes.KubeconfigPath) == "" {
		return nil, fmt.Errorf("%s is required to start controller process", envKubeconfig)
	}

	if err := validateControllerBinary(cfg.Controller.BinPath, opts.stat); err != nil {
		return nil, err
	}

	cmd := opts.command(cfg.Controller.BinPath, "start")
	cmd.Env = buildControllerEnv(cfg, opts.environ)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("prepare controller stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("prepare controller stderr pipe: %w", err)
	}

	startErr := cmd.Start()
	if startErr != nil {
		return nil, fmt.Errorf("start controller binary %q: %w", cfg.Controller.BinPath, startErr)
	}

	streamsDone := make(chan struct{})
	var streamGroup sync.WaitGroup
	streamGroup.Add(controllerStreamCount)
	go copyToTestLog(t, "stdout", stdout, &streamGroup)
	go copyToTestLog(t, "stderr", stderr, &streamGroup)
	go func() {
		streamGroup.Wait()
		close(streamsDone)
	}()

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
		close(waitDone)
	}()

	proc := &Process{
		path:        cfg.Controller.BinPath,
		pid:         cmd.Process.Pid,
		logf:        t.Logf,
		stopTimeout: opts.stopTimeout,
		cmd:         cmd,
		waitDone:    waitDone,
		streamsDone: streamsDone,
	}

	t.Logf("started controller process pid=%d path=%q", proc.pid, proc.path)
	t.Cleanup(func() {
		stopErr := proc.Stop()
		if stopErr != nil {
			t.Errorf("stop controller process: %v", stopErr)
		}
	})

	return proc, nil
}

func (p *Process) PID() int {
	if p == nil {
		return 0
	}

	return p.pid
}

func (p *Process) Skipped() bool {
	if p == nil {
		return false
	}

	return p.skipped
}

func (p *Process) Stop() error {
	if p == nil {
		return nil
	}

	p.stopOnce.Do(func() {
		p.stopErr = p.stop()
	})

	return p.stopErr
}

func (p *Process) stop() error {
	if p.skipped {
		return nil
	}

	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}

	if exited, waitErr := p.waitResultNonBlocking(); exited {
		return p.handleExitedProcess(waitErr)
	}

	return p.stopRunningProcess()
}

func (p *Process) handleExitedProcess(waitErr error) error {
	p.awaitStreams()
	if waitErr != nil {
		return fmt.Errorf("controller process exited before cleanup: %w", waitErr)
	}

	if p.logf != nil {
		p.logf("controller process pid=%d already exited", p.pid)
	}

	return nil
}

func (p *Process) stopRunningProcess() error {
	if p.logf != nil {
		p.logf("stopping controller process pid=%d", p.pid)
	}

	signalErr := p.cmd.Process.Signal(syscall.SIGTERM)
	if signalErr != nil && !errors.Is(signalErr, os.ErrProcessDone) {
		return fmt.Errorf("signal controller process pid=%d: %w", p.pid, signalErr)
	}

	return p.waitForExitAfterSignal()
}

func (p *Process) waitForExitAfterSignal() error {
	timer := time.NewTimer(p.stopTimeout)
	defer timer.Stop()

	select {
	case waitErr := <-p.waitDone:
		return p.handleSignalWaitResult(waitErr)
	case <-timer.C:
		return p.killAfterTimeout()
	}
}

func (p *Process) handleSignalWaitResult(waitErr error) error {
	p.awaitStreams()
	if waitErr != nil {
		return fmt.Errorf("wait for controller process pid=%d after SIGTERM: %w", p.pid, waitErr)
	}

	if p.logf != nil {
		p.logf("controller process pid=%d stopped", p.pid)
	}

	return nil
}

func (p *Process) killAfterTimeout() error {
	if p.logf != nil {
		p.logf("controller process pid=%d did not stop within %s; killing", p.pid, p.stopTimeout)
	}

	killErr := p.cmd.Process.Kill()
	if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		return fmt.Errorf("kill controller process pid=%d: %w", p.pid, killErr)
	}

	waitErr := <-p.waitDone
	p.awaitStreams()
	if waitErr != nil && !isSignaledExit(waitErr) {
		return fmt.Errorf("wait for killed controller process pid=%d: %w", p.pid, waitErr)
	}

	return nil
}

func isSignaledExit(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}

	waitStatus, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && waitStatus.Signaled()
}

func (p *Process) waitResultNonBlocking() (bool, error) {
	select {
	case err := <-p.waitDone:
		return true, err
	default:
		return false, nil
	}
}

func (p *Process) awaitStreams() {
	if p.streamsDone == nil {
		return
	}

	<-p.streamsDone
}

func validateControllerBinary(path string, stat func(string) (fs.FileInfo, error)) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%s is required", envControllerBin)
	}

	info, err := stat(path)
	if err == nil {
		if info.IsDir() {
			return fmt.Errorf("%s must point to a file, got directory %q", envControllerBin, path)
		}

		return nil
	}

	if os.IsNotExist(err) {
		return fmt.Errorf(
			"%s points to missing file %q; build the controller binary first with `direnv exec . make dist/bin`",
			envControllerBin,
			path,
		)
	}

	return fmt.Errorf("stat %s at %q: %w", envControllerBin, path, err)
}

func buildControllerEnv(cfg config.Config, environ []string) []string {
	if len(environ) == 0 {
		environ = os.Environ()
	}

	env := append([]string(nil), environ...)
	env = upsertEnv(env, envKubeconfig, cfg.Kubernetes.KubeconfigPath)
	env = upsertEnv(env, envK8sAPINoop, "false")
	env = upsertEnv(env, envOCIAPINoop, "false")

	if cfg.OCI.ConfigFile != "" {
		env = upsertEnv(env, envOCIConfigFile, cfg.OCI.ConfigFile)
		env = upsertEnv(env, envOCIConfigFileAlt, cfg.OCI.ConfigFile)
	}

	if cfg.OCI.ConfigProfile != "" {
		env = upsertEnv(env, envOCIConfigProfile, cfg.OCI.ConfigProfile)
		env = upsertEnv(env, envOCIConfigProfileAlt, cfg.OCI.ConfigProfile)
	}

	return env
}

func upsertEnv(environ []string, key string, value string) []string {
	if key == "" {
		return environ
	}

	entry := key + "=" + value
	for idx, current := range environ {
		currentKey, _, found := strings.Cut(current, "=")
		if found && currentKey == key {
			environ[idx] = entry
			return environ
		}
	}

	return append(environ, entry)
}

func copyToTestLog(t TestLogSink, streamName string, reader io.Reader, wg *sync.WaitGroup) {
	defer wg.Done()

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, streamScannerBufferSize), streamScannerMaxToken)
	for scanner.Scan() {
		t.Logf("controller %s: %s", streamName, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		t.Logf("controller %s read error: %v", streamName, err)
	}
}

func closedWaitChannel(err error) chan error {
	waitDone := make(chan error, 1)
	waitDone <- err
	close(waitDone)
	return waitDone
}

func closedSignalChannel() chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
