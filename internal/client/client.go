package client

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bduffany/kpf/internal/protocol"
)

const (
	daemonStartupWait   = 5 * time.Second
	socketRetryInterval = 100 * time.Millisecond

	completionCommand       = "__complete"
	completionNoDescCommand = "__completeNoDesc"

	portForwardUsage = "Usage:\n  kpf [port-forward] TYPE/NAME [options] [LOCAL_PORT:]REMOTE_PORT"
	helpUsage        = "Usage:\n  kpf TYPE/NAME [options] [LOCAL_PORT:]REMOTE_PORT [...[LOCAL_PORT_N:]REMOTE_PORT_N]"
	kubeIdentityKey  = "kpf-kube-identity"
	ttlEnvVar        = "KUBECTL_PORT_FORWARD_TTL"
	foregroundFlag   = "--fg"
	foregroundShort  = "-f"
	listFlag         = "--list"
	aliasFlag        = "--alias"
	aliasShort       = "-a"
	fzfInstallURL    = "https://github.com/junegunn/fzf#installation"
)

var valueFlags = map[string]bool{
	"--address":               true,
	"--pod-running-timeout":   true,
	"--as":                    true,
	"--as-group":              true,
	"--as-uid":                true,
	"--cache-dir":             true,
	"--certificate-authority": true,
	"--client-certificate":    true,
	"--client-key":            true,
	"--cluster":               true,
	"--context":               true,
	"--kubeconfig":            true,
	"--namespace":             true,
	"--password":              true,
	"--profile":               true,
	"--profile-output":        true,
	"--request-timeout":       true,
	"--server":                true,
	"--tls-server-name":       true,
	"--token":                 true,
	"--user":                  true,
	"--username":              true,
	"--v":                     true,
	"--vmodule":               true,
	"--log-file":              true,
	"--log-file-max-size":     true,
}

type kubeSelection struct {
	configArgs       []string
	namespace        string
	namespacePresent bool
}

type kubeConfigView struct {
	CurrentContext string `json:"current-context"`
	Contexts       []struct {
		Name    string `json:"name"`
		Context struct {
			Cluster   string `json:"cluster"`
			User      string `json:"user"`
			Namespace string `json:"namespace,omitempty"`
		} `json:"context"`
	} `json:"contexts"`
	Clusters []struct {
		Name    string `json:"name"`
		Cluster struct {
			Server string `json:"server,omitempty"`
		} `json:"cluster"`
	} `json:"clusters"`
}

type resolvedKubeIdentity struct {
	Context   string `json:"context,omitempty"`
	Cluster   string `json:"cluster,omitempty"`
	Server    string `json:"server,omitempty"`
	User      string `json:"user,omitempty"`
	Namespace string `json:"namespace"`
}

type aliasAction int

const (
	aliasActionNone aliasAction = iota
	aliasActionSet
)

// IsCompletionCommand reports whether arg is a shell completion command.
func IsCompletionCommand(arg string) bool {
	return arg == completionCommand || arg == completionNoDescCommand
}

// RunCompletion proxies shell completion requests to kubectl port-forward.
func RunCompletion(args []string) error {
	if len(args) == 0 || !IsCompletionCommand(args[0]) {
		return errors.New("completion command must start with __complete or __completeNoDesc")
	}

	kubectlArgs := make([]string, 0, len(args)+1)
	kubectlArgs = append(kubectlArgs, args[0])
	if len(args) < 2 || args[1] != "port-forward" {
		kubectlArgs = append(kubectlArgs, "port-forward")
	}
	kubectlArgs = append(kubectlArgs, args[1:]...)

	cmd := exec.Command("kubectl", kubectlArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// IsHelpCommand reports whether args request help text.
func IsHelpCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "help" {
		return true
	}
	if len(args) >= 2 && args[0] == "port-forward" && args[1] == "help" {
		return true
	}

	if args[0] == "port-forward" {
		args = args[1:]
	}
	literalMode := false
	for _, arg := range args {
		if !literalMode && arg == "--" {
			literalMode = true
			continue
		}
		if !literalMode && (arg == "--help" || arg == "-h") {
			return true
		}
	}
	return false
}

// RunHelp prints kubectl port-forward help adapted for kpf.
func RunHelp() error {
	cmd := exec.Command("kubectl", "port-forward", "--help")
	output, err := cmd.CombinedOutput()
	if len(output) > 0 {
		text := formatHelpOutput(string(output))
		_, _ = fmt.Fprint(os.Stdout, text)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return err
		}
		return fmt.Errorf("run kubectl port-forward --help: %w", err)
	}
	return nil
}

func formatHelpOutput(raw string) string {
	text := strings.ReplaceAll(raw, "kubectl port-forward", "kpf")
	text = strings.TrimRight(text, "\n")
	ttlDefault := protocol.DefaultSessionTTL.String()
	return text + "\n\nkpf options:\n" +
		"    --alias, -a NAME:\n" +
		"\tSave or update NAME from resolved args and exit.\n\n" +
		"    --list:\n" +
		"\tPrint available port-forward targets discovered by kpf and exit.\n\n" +
		"    --fg, -f:\n" +
		"\tRun kubectl port-forward in the foreground. Do not use the kpf daemon.\n\n" +
		"    --ttl=" + ttlDefault + ":\n" +
		"\tSession TTL for daemon-managed forwards. Accepts Go duration values like 30m or 1h.\n" +
		"\tCan also be set via KUBECTL_PORT_FORWARD_TTL (the --ttl flag takes higher priority).\n\n" +
		"Environment variables:\n" +
		"    " + ttlEnvVar + ":\n" +
		"\tDefault session TTL (Go duration) used when --ttl is not set.\n"
}

func resolveKubeIdentity(args []string) (string, error) {
	selection, err := extractKubeSelection(args)
	if err != nil {
		return "", err
	}

	kubectlArgs := make([]string, 0, len(selection.configArgs)+6)
	kubectlArgs = append(kubectlArgs, selection.configArgs...)
	kubectlArgs = append(kubectlArgs, "config", "view", "--minify", "--flatten", "-o", "json")

	cmd := exec.Command("kubectl", kubectlArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if msg != "" {
			return "", fmt.Errorf("resolve kube context: %w: %s", err, msg)
		}
		return "", fmt.Errorf("resolve kube context: %w", err)
	}

	return canonicalizeKubeIdentity(output, selection)
}

func extractKubeSelection(args []string) (kubeSelection, error) {
	selection := kubeSelection{}
	literalMode := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !literalMode && arg == "--" {
			literalMode = true
			continue
		}
		if literalMode || !strings.HasPrefix(arg, "-") {
			continue
		}

		switch {
		case arg == "--context" || arg == "--cluster" || arg == "--user" || arg == "--kubeconfig":
			if i+1 >= len(args) {
				return kubeSelection{}, fmt.Errorf("missing value for flag %q", arg)
			}
			selection.configArgs = append(selection.configArgs, arg, args[i+1])
			i++
		case strings.HasPrefix(arg, "--context=") || strings.HasPrefix(arg, "--cluster=") ||
			strings.HasPrefix(arg, "--user=") || strings.HasPrefix(arg, "--kubeconfig="):
			selection.configArgs = append(selection.configArgs, arg)
		case arg == "--namespace":
			if i+1 >= len(args) {
				return kubeSelection{}, fmt.Errorf("missing value for flag %q", arg)
			}
			selection.namespace = args[i+1]
			selection.namespacePresent = true
			i++
		case strings.HasPrefix(arg, "--namespace="):
			selection.namespace = strings.TrimPrefix(arg, "--namespace=")
			selection.namespacePresent = true
		case arg == "-n":
			if i+1 >= len(args) {
				return kubeSelection{}, fmt.Errorf("missing value for flag %q", arg)
			}
			selection.namespace = args[i+1]
			selection.namespacePresent = true
			i++
		case strings.HasPrefix(arg, "-n="):
			selection.namespace = strings.TrimPrefix(arg, "-n=")
			selection.namespacePresent = true
		}
	}

	return selection, nil
}

func canonicalizeKubeIdentity(rawConfig []byte, selection kubeSelection) (string, error) {
	var cfg kubeConfigView
	if err := json.Unmarshal(rawConfig, &cfg); err != nil {
		return "", fmt.Errorf("decode kube config: %w", err)
	}

	ctxName, ctxCluster, ctxUser, ctxNamespace, err := activeContext(cfg)
	if err != nil {
		return "", err
	}

	namespace := "default"
	if ctxNamespace != "" {
		namespace = ctxNamespace
	}
	if selection.namespacePresent {
		namespace = selection.namespace
	}

	identity := resolvedKubeIdentity{
		Context:   ctxName,
		Cluster:   ctxCluster,
		Server:    findClusterServer(cfg, ctxCluster),
		User:      ctxUser,
		Namespace: namespace,
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("encode kube identity: %w", err)
	}
	return string(encoded), nil
}

func activeContext(cfg kubeConfigView) (name, cluster, user, namespace string, err error) {
	if len(cfg.Contexts) == 0 {
		return "", "", "", "", errors.New("could not resolve kube context: no contexts found")
	}

	if cfg.CurrentContext != "" {
		for _, entry := range cfg.Contexts {
			if entry.Name == cfg.CurrentContext {
				return entry.Name, entry.Context.Cluster, entry.Context.User, entry.Context.Namespace, nil
			}
		}
	}

	entry := cfg.Contexts[0]
	return entry.Name, entry.Context.Cluster, entry.Context.User, entry.Context.Namespace, nil
}

func findClusterServer(cfg kubeConfigView, clusterName string) string {
	for _, cluster := range cfg.Clusters {
		if cluster.Name == clusterName {
			return cluster.Cluster.Server
		}
	}
	if len(cfg.Clusters) == 1 {
		return cfg.Clusters[0].Cluster.Server
	}
	return ""
}

// Run executes client mode: ensure the daemon is running, request/ensure a
// matching port-forward session, and print the local port.
func Run(args []string) error {
	var err error
	args, aliasMode, aliasName, err := extractAliasAction(args)
	if err != nil {
		return err
	}
	if aliasMode == aliasActionSet {
		return runAliasSet(aliasName, args)
	}

	args, list := extractList(args)
	args, foreground := extractForeground(args)
	if list {
		return runList(args)
	}

	if foreground {
		return runForeground(args)
	}

	req, selectedPortName, _, err := resolveRequestWithPickerFallback(args)
	if err != nil {
		return err
	}

	resp, err := sendRequest(req)
	if err != nil && shouldStartDaemon(err) {
		if err := startDaemonProcess(); err != nil {
			return err
		}
		if err := waitForDaemon(protocol.SocketPath(), daemonStartupWait); err != nil {
			return err
		}
		resp, err = sendRequest(req)
	}
	if err != nil {
		return err
	}
	if !resp.OK {
		if resp.Error == "" {
			return errors.New("daemon returned an unknown error")
		}
		return errors.New(resp.Error)
	}
	if resp.LocalPort <= 0 {
		return errors.New("daemon did not return a local port")
	}

	recordResolvedHistory(req.Args, selectedPortName)
	fmt.Println(resp.LocalPort)
	return nil
}

func runForeground(args []string) error {
	req, selectedPortName, _, err := resolveRequestWithPickerFallback(args)
	if err != nil {
		return err
	}

	recordResolvedHistory(req.Args, selectedPortName)
	return runKubectlPortForward(req.Args)
}

func resolveRequestWithPickerFallback(args []string) (*protocol.Request, string, bool, error) {
	req, err := parseRequest(args)
	if err == nil {
		return req, "", false, nil
	}

	resolvedArgs, selectedPortName, usedPicker, pickerErr := resolveArgsWithPicker(args)
	if pickerErr != nil {
		return nil, "", false, mapPickerError(pickerErr)
	}
	if !usedPicker {
		return nil, "", false, err
	}

	req, err = parseRequest(resolvedArgs)
	if err != nil {
		return nil, "", false, err
	}
	return req, selectedPortName, true, nil
}

func runAliasSet(aliasName string, args []string) error {
	resolveArgs := args
	if len(resolveArgs) == 0 {
		resolveArgs = []string{aliasName}
	}

	req, selectedPortName, _, err := resolveRequestWithPickerFallback(resolveArgs)
	if err != nil {
		return err
	}

	resolvedArgs, err := fullyResolveNormalizedArgs(req.Args)
	if err != nil {
		resolvedArgs = req.Args
	}
	if err := saveAlias(aliasName, resolvedArgs, selectedPortName); err != nil {
		return fmt.Errorf("save alias: %w", err)
	}
	fmt.Printf("saved alias %q\n", aliasName)
	return nil
}

func mapPickerError(err error) error {
	if errors.Is(err, errFZFUnavailable) {
		return usageWithFZFInstallTipError()
	}
	return err
}

func usageWithFZFInstallTipError() error {
	return errors.New(helpUsage + "\n\nTIP: install fzf to get search: " + fzfInstallURL)
}

func runList(args []string) error {
	intent, err := parsePickerIntent(args)
	if err != nil {
		return err
	}

	identity, err := resolveKubeIdentityStruct(intent.baseArgs)
	if err != nil {
		return err
	}
	candidates, err := discoverPortForwardCandidates(intent.selection)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return errors.New("no port-forward candidates found in the selected context")
	}
	commands, err := buildResolvedCommands(intent.baseArgs, identity, candidates)
	if err != nil {
		return err
	}
	history, err := loadHistory()
	if err != nil {
		history = map[string]int64{}
	}
	commands = orderResolvedCommandsByHistory(commands, history)
	for _, command := range commands {
		fmt.Println(command.Display)
	}
	return nil
}

func extractAliasAction(args []string) ([]string, aliasAction, string, error) {
	filtered := make([]string, 0, len(args))
	action := aliasActionNone
	aliasName := ""
	literalMode := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !literalMode && arg == "--" {
			literalMode = true
			filtered = append(filtered, arg)
			continue
		}

		if !literalMode && (arg == aliasFlag || arg == aliasShort) {
			if action != aliasActionNone {
				return nil, aliasActionNone, "", errors.New("--alias may only be provided once")
			}
			if i+1 >= len(args) || args[i+1] == "--" || strings.HasPrefix(args[i+1], "-") {
				return nil, aliasActionNone, "", fmt.Errorf("missing value for flag %q", arg)
			}
			aliasName = args[i+1]
			action = aliasActionSet
			i++
			continue
		}
		if !literalMode && strings.HasPrefix(arg, aliasFlag+"=") {
			if action != aliasActionNone {
				return nil, aliasActionNone, "", errors.New("--alias may only be provided once")
			}
			aliasName = strings.TrimPrefix(arg, aliasFlag+"=")
			if aliasName == "" {
				return nil, aliasActionNone, "", errors.New("missing value for flag \"--alias\"")
			}
			action = aliasActionSet
			continue
		}
		if !literalMode && strings.HasPrefix(arg, aliasShort+"=") {
			if action != aliasActionNone {
				return nil, aliasActionNone, "", errors.New("--alias may only be provided once")
			}
			aliasName = strings.TrimPrefix(arg, aliasShort+"=")
			if aliasName == "" {
				return nil, aliasActionNone, "", errors.New("missing value for flag \"-a\"")
			}
			action = aliasActionSet
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered, action, aliasName, nil
}

func extractList(args []string) ([]string, bool) {
	filtered := make([]string, 0, len(args))
	list := false
	literalMode := false

	for _, arg := range args {
		if !literalMode && arg == "--" {
			literalMode = true
			filtered = append(filtered, arg)
			continue
		}
		if !literalMode && arg == listFlag {
			list = true
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered, list
}

func extractForeground(args []string) ([]string, bool) {
	filtered := make([]string, 0, len(args))
	foreground := false
	literalMode := false

	for _, arg := range args {
		if !literalMode && arg == "--" {
			literalMode = true
			filtered = append(filtered, arg)
			continue
		}
		if !literalMode && (arg == foregroundFlag || arg == foregroundShort) {
			foreground = true
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered, foreground
}

func fullyResolveNormalizedArgs(args []string) ([]string, error) {
	identity, err := resolveKubeIdentityStruct(args)
	if err != nil {
		return nil, err
	}
	resource, resourceArgIndex, portArgIndex, portArg, err := findResourceAndPort(args)
	if err != nil {
		return nil, err
	}
	_, remotePort, err := parsePortArg(portArg)
	if err != nil {
		return nil, err
	}
	baseArgs := make([]string, 0, len(args)-2)
	for i, arg := range args {
		if i == resourceArgIndex || i == portArgIndex {
			continue
		}
		baseArgs = append(baseArgs, arg)
	}

	return buildResolvedPortForwardArgs(baseArgs, identity, portForwardCandidate{
		Namespace:  identity.Namespace,
		Resource:   resource,
		RemotePort: remotePort,
	})
}

func recordResolvedHistory(args []string, portName string) {
	resolvedArgs, err := fullyResolveNormalizedArgs(args)
	if err != nil {
		resolvedArgs = args
	}
	_ = recordHistory(resolvedArgs, portName)
}

func parseRequest(args []string) (*protocol.Request, error) {
	if len(args) == 0 {
		return nil, errors.New(portForwardUsage)
	}
	if args[0] == "port-forward" {
		args = args[1:]
	}

	args, ttl, err := extractTTL(args)
	if err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, errors.New(portForwardUsage)
	}

	resource, _, portArgIndex, portArg, err := findResourceAndPort(args)
	if err != nil {
		return nil, err
	}
	localPort, remotePort, err := parsePortArg(portArg)
	if err != nil {
		return nil, err
	}

	normalized := append([]string(nil), args...)
	if localPort == nil {
		normalized[portArgIndex] = ":" + remotePort
	} else {
		normalized[portArgIndex] = fmt.Sprintf("%d:%s", *localPort, remotePort)
	}
	keyArgs := append([]string(nil), normalized...)
	keyArgs = append(keyArgs, fmt.Sprintf("kpf-session-ttl-nanos=%d", ttl.Nanoseconds()))
	kubeIdentity, err := resolveKubeIdentity(normalized)
	if err != nil {
		return nil, err
	}
	keyArgs = append(keyArgs, fmt.Sprintf("%s=%s", kubeIdentityKey, kubeIdentity))

	return &protocol.Request{
		Action:          "ensure",
		Key:             requestKey(keyArgs),
		Resource:        resource,
		RemotePort:      remotePort,
		LocalPort:       localPort,
		Args:            normalized,
		SessionTTLNanos: ttl.Nanoseconds(),
	}, nil
}

func extractTTL(args []string) ([]string, time.Duration, error) {
	ttl := protocol.DefaultSessionTTL
	if raw, ok := os.LookupEnv(ttlEnvVar); ok {
		parsed, err := parseTTLValue(raw, ttlEnvVar)
		if err != nil {
			return nil, 0, err
		}
		ttl = parsed
	}
	filtered := make([]string, 0, len(args))
	literalMode := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !literalMode && arg == "--" {
			literalMode = true
			filtered = append(filtered, arg)
			continue
		}
		if !literalMode && arg == "--ttl" {
			if i+1 >= len(args) {
				return nil, 0, errors.New("missing value for flag \"--ttl\"")
			}
			parsed, err := parseTTL(args[i+1])
			if err != nil {
				return nil, 0, err
			}
			ttl = parsed
			i++
			continue
		}
		if !literalMode && strings.HasPrefix(arg, "--ttl=") {
			parsed, err := parseTTL(strings.TrimPrefix(arg, "--ttl="))
			if err != nil {
				return nil, 0, err
			}
			ttl = parsed
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered, ttl, nil
}

func parseTTL(raw string) (time.Duration, error) {
	return parseTTLValue(raw, "--ttl")
}

func parseTTLValue(raw string, source string) (time.Duration, error) {
	if raw == "" {
		if source == "--ttl" {
			return 0, errors.New("missing value for flag \"--ttl\"")
		}
		return 0, fmt.Errorf("missing value for %s", source)
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil {
		if source == "--ttl" {
			return 0, fmt.Errorf("invalid value for --ttl %q: %w", raw, err)
		}
		return 0, fmt.Errorf("invalid value for %s %q: %w", source, raw, err)
	}
	if ttl <= 0 {
		if source == "--ttl" {
			return 0, errors.New("--ttl must be greater than 0")
		}
		return 0, fmt.Errorf("%s must be greater than 0", source)
	}
	return ttl, nil
}

func findResourceAndPort(args []string) (resource string, resourceArgIndex int, portArgIndex int, portArg string, err error) {
	resourceArgIndex = -1
	portArgIndex = -1
	seenResource := false
	literalMode := false

	for i := 0; i < len(args); i++ {
		a := args[i]
		if !literalMode && a == "--" {
			literalMode = true
			continue
		}
		if !literalMode && strings.HasPrefix(a, "-") {
			if flagTakesValue(a) {
				if strings.Contains(a, "=") {
					continue
				}
				i++
				if i >= len(args) {
					return "", -1, -1, "", fmt.Errorf("missing value for flag %q", a)
				}
			}
			continue
		}
		if !seenResource {
			resource = a
			resourceArgIndex = i
			seenResource = true
			continue
		}
		if portArgIndex >= 0 {
			return "", -1, -1, "", errors.New("only one port mapping is currently supported")
		}
		portArgIndex = i
		portArg = a
	}

	if resource == "" {
		return "", -1, -1, "", errors.New("missing resource argument (for example pod/name or svc/name)")
	}
	if portArgIndex < 0 {
		return "", -1, -1, "", errors.New("missing port mapping argument")
	}
	return resource, resourceArgIndex, portArgIndex, portArg, nil
}

func flagTakesValue(flag string) bool {
	if strings.HasPrefix(flag, "--") {
		if strings.Contains(flag, "=") {
			return false
		}
		return valueFlags[flag]
	}
	switch flag {
	case "-n", "-s", "-v":
		return true
	default:
		return false
	}
}

func parsePortArg(spec string) (*int, string, error) {
	if strings.Count(spec, ":") == 0 {
		if spec == "" {
			return nil, "", errors.New("port mapping cannot be empty")
		}
		return nil, spec, nil
	}
	if strings.Count(spec, ":") != 1 {
		return nil, "", fmt.Errorf("invalid port mapping %q", spec)
	}
	parts := strings.SplitN(spec, ":", 2)
	if parts[1] == "" {
		return nil, "", fmt.Errorf("invalid port mapping %q", spec)
	}
	if parts[0] == "" {
		return nil, parts[1], nil
	}
	p, err := strconv.Atoi(parts[0])
	if err != nil || p <= 0 || p > 65535 {
		return nil, "", fmt.Errorf("invalid local port %q", parts[0])
	}
	return &p, parts[1], nil
}

func requestKey(args []string) string {
	s := sha256.Sum256([]byte(strings.Join(args, "\x00")))
	return hex.EncodeToString(s[:])
}

func sendRequest(req *protocol.Request) (*protocol.Response, error) {
	conn, err := net.DialTimeout("unix", protocol.SocketPath(), protocol.SocketDialTimeout)
	if err != nil {
		return nil, fmt.Errorf("connect daemon: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(protocol.RequestTimeout))

	enc := json.NewEncoder(conn)
	if err := enc.Encode(req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	var resp protocol.Response
	dec := json.NewDecoder(conn)
	if err := dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return &resp, nil
}

func shouldStartDaemon(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if errors.Is(opErr.Err, syscall.ENOENT) || errors.Is(opErr.Err, syscall.ECONNREFUSED) {
			return true
		}
	}
	return errors.Is(err, os.ErrNotExist)
}

func startDaemonProcess() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	cmd := exec.Command(exe, protocol.DaemonArg)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	return cmd.Process.Release()
}

var runKubectlPortForward = func(args []string) error {
	cmd := exec.Command("kubectl", append([]string{"port-forward"}, args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func waitForDaemon(sock string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("unix", sock, protocol.SocketDialTimeout)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wait for daemon: %w", err)
		}
		time.Sleep(socketRetryInterval)
	}
}
