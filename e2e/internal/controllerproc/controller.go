package controllerproc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/gemyago/oke-gateway-api/e2e/internal/config"
	"github.com/gemyago/oke-gateway-api/e2e/internal/diag"
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
	controllerLogFileMode   = 0o600
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
	openFile    func(string, int, fs.FileMode) (*os.File, error)
	logFilePath string
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
	exitDone    chan struct{}
	exitErr     error
	streamsDone chan struct{}
	cleanup     func()
	stopOnce    sync.Once
	stopErr     error
	logMu       sync.Mutex
	logLines    []string
	logWait     chan struct{}
	streamLog   *os.File
	streamLogMu sync.Mutex
}

func NewStartOptions() *StartOptions {
	return &StartOptions{
		command:     exec.Command,
		openFile:    os.OpenFile,
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

func (opts *StartOptions) WithLogFilePath(path string) *StartOptions {
	opts.logFilePath = path
	return opts
}

func (opts *StartOptions) WithOpenFile(fn func(string, int, fs.FileMode) (*os.File, error)) *StartOptions {
	opts.openFile = fn
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

	opts = normalizeStartOptions(opts)

	if cfg.Controller.SkipStart {
		t.Logf("controller start skipped because %s=true", envSkipController)
		return &Process{
			path:        cfg.Controller.BinPath,
			skipped:     true,
			logf:        t.Logf,
			stopTimeout: opts.stopTimeout,
			exitDone:    closedSignalChannel(),
			streamsDone: closedSignalChannel(),
			cleanup:     func() {},
			logWait:     make(chan struct{}),
		}, nil
	}

	logFile, logFilePath, err := openControllerLogFile(opts)
	if err != nil {
		return nil, err
	}

	cmd, stdout, stderr, cleanupControllerKubeconfig, err := prepareStartedCommand(cfg, opts)
	if err != nil {
		_ = logFile.Close()
		return nil, err
	}

	streamsDone := make(chan struct{})
	proc := &Process{
		path:        cfg.Controller.BinPath,
		pid:         cmd.Process.Pid,
		logf:        t.Logf,
		stopTimeout: opts.stopTimeout,
		cmd:         cmd,
		exitDone:    make(chan struct{}),
		streamsDone: streamsDone,
		cleanup:     cleanupControllerKubeconfig,
		logWait:     make(chan struct{}),
		streamLog:   logFile,
	}

	var streamGroup sync.WaitGroup
	streamGroup.Add(controllerStreamCount)
	go copyToControllerLog(proc, "stdout", stdout, &streamGroup)
	go copyToControllerLog(proc, "stderr", stderr, &streamGroup)
	go func() {
		streamGroup.Wait()
		close(streamsDone)
		// Drain both pipes before Wait. os/exec documents that calling Wait before
		// all reads from StdoutPipe/StderrPipe complete can race with pipe closure
		// and drop the last log lines from fast-exiting processes.
		proc.exitErr = cmd.Wait()
		close(proc.exitDone)
	}()

	t.Logf("started controller process pid=%d path=%q controllerLog=%q", proc.pid, proc.path, logFilePath)
	t.Cleanup(func() {
		stopErr := proc.Stop()
		if stopErr != nil {
			t.Errorf("stop controller process: %v", stopErr)
		}
	})

	return proc, nil
}

func normalizeStartOptions(opts *StartOptions) *StartOptions {
	if opts == nil {
		opts = NewStartOptions()
	}

	if opts.command == nil {
		opts.command = exec.Command
	}

	if opts.openFile == nil {
		opts.openFile = os.OpenFile
	}

	if opts.stat == nil {
		opts.stat = os.Stat
	}

	if opts.stopTimeout <= 0 {
		opts.stopTimeout = defaultStopTimeout
	}

	return opts
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
	defer p.cleanupResources()

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

func (p *Process) WaitForLog(ctx context.Context, fragment string) error {
	if p == nil {
		return errors.New("controller process is required")
	}

	fragment = strings.TrimSpace(fragment)
	if fragment == "" {
		return errors.New("controller log fragment is required")
	}

	for {
		found, logWait := p.waitForLogState(fragment)
		if found {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for controller log %q: %w", fragment, ctx.Err())
		case <-logWait:
		case <-p.exitDone:
			p.awaitStreams()
			if p.containsLogLine(fragment) {
				return nil
			}

			if p.exitErr != nil {
				return fmt.Errorf("controller process exited before log %q: %w", fragment, p.exitErr)
			}

			return fmt.Errorf("controller process exited before log %q", fragment)
		}
	}
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
	case <-p.exitDone:
		return p.handleSignalWaitResult(p.exitErr)
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

	<-p.exitDone
	waitErr := p.exitErr
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
	case <-p.exitDone:
		return true, p.exitErr
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

func (p *Process) appendLogLine(line string) {
	if p == nil {
		return
	}

	p.logMu.Lock()
	p.logLines = append(p.logLines, line)
	logWait := p.logWait
	p.logWait = make(chan struct{})
	p.logMu.Unlock()

	close(logWait)
}

func (p *Process) waitForLogState(fragment string) (bool, chan struct{}) {
	p.logMu.Lock()
	defer p.logMu.Unlock()

	for _, line := range p.logLines {
		if strings.Contains(line, fragment) {
			return true, nil
		}
	}

	return false, p.logWait
}

func (p *Process) containsLogLine(fragment string) bool {
	p.logMu.Lock()
	defer p.logMu.Unlock()

	for _, line := range p.logLines {
		if strings.Contains(line, fragment) {
			return true
		}
	}

	return false
}

func (p *Process) cleanupResources() {
	if p == nil {
		return
	}

	if p.cleanup != nil {
		p.cleanup()
		p.cleanup = nil
	}

	if p.streamLog != nil {
		_ = p.streamLog.Close()
		p.streamLog = nil
	}
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

func prepareStartedCommand(
	cfg config.Config,
	opts *StartOptions,
) (*exec.Cmd, io.ReadCloser, io.ReadCloser, func(), error) {
	if err := validateControllerBinary(cfg.Controller.BinPath, opts.stat); err != nil {
		return nil, nil, nil, func() {}, err
	}

	controllerKubeconfigPath, cleanupControllerKubeconfig, err := prepareControllerKubeconfig(
		cfg,
		opts.environ,
	)
	if err != nil {
		return nil, nil, nil, func() {}, err
	}

	cmd := opts.command(cfg.Controller.BinPath, "start")
	cmd.Env = buildControllerEnv(cfg, opts.environ, controllerKubeconfigPath)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanupControllerKubeconfig()
		return nil, nil, nil, func() {}, fmt.Errorf("prepare controller stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		cleanupControllerKubeconfig()
		return nil, nil, nil, func() {}, fmt.Errorf("prepare controller stderr pipe: %w", err)
	}

	if startErr := cmd.Start(); startErr != nil {
		cleanupControllerKubeconfig()
		return nil, nil, nil, func() {}, fmt.Errorf(
			"start controller binary %q: %w",
			cfg.Controller.BinPath,
			startErr,
		)
	}

	return cmd, stdout, stderr, cleanupControllerKubeconfig, nil
}

func prepareControllerKubeconfig(cfg config.Config, environ []string) (string, func(), error) {
	if strings.TrimSpace(cfg.Kubernetes.Context) == "" {
		return cfg.Kubernetes.KubeconfigPath, func() {}, nil
	}

	rawConfig, err := loadKubeconfig(cfg.Kubernetes.KubeconfigPath, environ)
	if err != nil {
		return "", func() {}, fmt.Errorf(
			"load kubeconfig for controller context %q: %w",
			cfg.Kubernetes.Context,
			err,
		)
	}

	shapedConfig, err := shapeKubeconfigForContext(rawConfig, cfg.Kubernetes.Context)
	if err != nil {
		return "", func() {}, err
	}

	tempDir, err := os.MkdirTemp("", "oke-e2e-controller-kubeconfig-")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temporary kubeconfig directory: %w", err)
	}

	kubeconfigPath := filepath.Join(tempDir, "config")
	if writeErr := clientcmd.WriteToFile(*shapedConfig, kubeconfigPath); writeErr != nil {
		_ = os.RemoveAll(tempDir)
		return "", func() {}, fmt.Errorf(
			"write controller kubeconfig for context %q: %w",
			cfg.Kubernetes.Context,
			writeErr,
		)
	}

	return kubeconfigPath, func() {
		_ = os.RemoveAll(tempDir)
	}, nil
}

func loadKubeconfig(kubeconfigPath string, environ []string) (*clientcmdapi.Config, error) {
	loader := clientcmd.NewDefaultClientConfigLoadingRules()
	if strings.TrimSpace(kubeconfigPath) != "" {
		loader.ExplicitPath = kubeconfigPath
	} else if inheritedPath, ok := envValue(environ, envKubeconfig); ok {
		loader.Precedence = filepath.SplitList(inheritedPath)
	}

	return loader.Load()
}

func shapeKubeconfigForContext(rawConfig *clientcmdapi.Config, contextName string) (*clientcmdapi.Config, error) {
	if rawConfig == nil {
		return nil, errors.New("kubeconfig is required")
	}

	selectedContext, contextFound := rawConfig.Contexts[contextName]
	if !contextFound {
		return nil, fmt.Errorf("kubeconfig missing required context %q", contextName)
	}

	shapedConfig := clientcmdapi.NewConfig()
	shapedConfig.CurrentContext = contextName
	shapedConfig.Contexts[contextName] = selectedContext.DeepCopy()

	if selectedContext.Cluster != "" {
		cluster, clusterFound := rawConfig.Clusters[selectedContext.Cluster]
		if !clusterFound {
			return nil, fmt.Errorf(
				"kubeconfig context %q references missing cluster %q",
				contextName,
				selectedContext.Cluster,
			)
		}

		shapedConfig.Clusters[selectedContext.Cluster] = cluster.DeepCopy()
	}

	if selectedContext.AuthInfo != "" {
		authInfo, authInfoFound := rawConfig.AuthInfos[selectedContext.AuthInfo]
		if !authInfoFound {
			return nil, fmt.Errorf(
				"kubeconfig context %q references missing auth info %q",
				contextName,
				selectedContext.AuthInfo,
			)
		}

		shapedConfig.AuthInfos[selectedContext.AuthInfo] = authInfo.DeepCopy()
	}

	return shapedConfig, nil
}

func buildControllerEnv(cfg config.Config, environ []string, controllerKubeconfigPath string) []string {
	if len(environ) == 0 {
		environ = os.Environ()
	}

	env := append([]string(nil), environ...)
	if controllerKubeconfigPath != "" {
		env = upsertEnv(env, envKubeconfig, controllerKubeconfigPath)
	}
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

func envValue(environ []string, key string) (string, bool) {
	for _, entry := range environ {
		currentKey, currentValue, found := strings.Cut(entry, "=")
		if found && currentKey == key {
			return currentValue, true
		}
	}

	return "", false
}

func openControllerLogFile(opts *StartOptions) (*os.File, string, error) {
	logFilePath := strings.TrimSpace(opts.logFilePath)
	if logFilePath == "" {
		logFilePath = diag.ProjectPath("controller.log")
	}

	logFile, err := opts.openFile(logFilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, controllerLogFileMode)
	if err != nil {
		return nil, "", fmt.Errorf("open controller log file %q: %w", logFilePath, err)
	}

	return logFile, logFilePath, nil
}

func copyToControllerLog(proc *Process, streamName string, reader io.Reader, wg *sync.WaitGroup) {
	defer wg.Done()

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, streamScannerBufferSize), streamScannerMaxToken)
	for scanner.Scan() {
		line := fmt.Sprintf("controller %s: %s", streamName, scanner.Text())
		proc.appendLogLine(line)
		proc.writeStreamLogLine(line)
	}

	if err := scanner.Err(); err != nil {
		line := fmt.Sprintf("controller %s read error: %v", streamName, err)
		proc.appendLogLine(line)
		proc.writeStreamLogLine(line)
	}
}

func closedSignalChannel() chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

func (p *Process) writeStreamLogLine(line string) {
	if p == nil || p.streamLog == nil {
		return
	}

	p.streamLogMu.Lock()
	defer p.streamLogMu.Unlock()

	_, _ = fmt.Fprintln(p.streamLog, line)
}
