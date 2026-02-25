package client

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

const kubectlDiscoveryResources = "services,pods"

var errFZFUnavailable = errors.New("fzf is not installed")

type pickerIntent struct {
	needsPicker  bool
	baseArgs     []string
	initialQuery string
	selection    kubeSelection
}

type portForwardCandidate struct {
	Namespace  string
	Resource   string
	RemotePort string
	PortName   string
}

type resolvedCommand struct {
	ResolvedArgs []string
	Display      string
	HistoryKey   string
	LastUsedUnix int64
	PortName     string
}

type preferredAlias struct {
	Name  string
	Entry aliasEntry
}

type pickerSelection struct {
	Args     []string `json:"args"`
	PortName string   `json:"port_name,omitempty"`
}

type kubectlResourceList struct {
	Items []kubectlResource `json:"items"`
}

type kubectlResource struct {
	Kind     string          `json:"kind"`
	Metadata kubectlMetadata `json:"metadata"`
	Spec     json.RawMessage `json:"spec"`
}

type kubectlMetadata struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type kubectlServiceSpec struct {
	Ports []kubectlServicePort `json:"ports"`
}

type kubectlServicePort struct {
	Name string `json:"name,omitempty"`
	Port int    `json:"port"`
}

type kubectlPodSpec struct {
	Containers []kubectlContainer `json:"containers"`
}

type kubectlContainer struct {
	Ports []kubectlContainerPort `json:"ports"`
}

type kubectlContainerPort struct {
	Name          string `json:"name,omitempty"`
	ContainerPort int    `json:"containerPort"`
}

var runKubectlCombinedOutput = func(args []string) ([]byte, error) {
	cmd := exec.Command("kubectl", args...)
	return cmd.CombinedOutput()
}

var canUseInteractivePicker = func() bool {
	return isTerminal(os.Stdout) && isTerminal(os.Stderr)
}

func fzfArgs(initialQuery string, header string) []string {
	fzfArgs := []string{
		"--height=80%",
		"--layout=reverse",
		"--border",
		"--ansi",
		"--delimiter=\t",
		"--with-nth=1",
		"--nth=1",
		"--no-mouse",
		// Preserve input ordering so streamed history rows keep priority.
		"--no-sort",
		"--prompt=kpf> ",
		"--header", header,
		"--no-hscroll",
	}
	if initialQuery != "" {
		fzfArgs = append(fzfArgs, "--query", initialQuery)
	}
	return fzfArgs
}

func runFZFWithInput(input io.Reader, initialQuery string, header string) (string, error) {
	if _, err := exec.LookPath("fzf"); err != nil {
		return "", errFZFUnavailable
	}
	if !canUseInteractivePicker() {
		return "", errors.New("interactive selection requires stdout and stderr to be attached terminals")
	}

	cmd := exec.Command("fzf", fzfArgs(initialQuery, header)...)
	cmd.Stdin = input
	cmd.Stderr = os.Stderr
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && (exitErr.ExitCode() == 1 || exitErr.ExitCode() == 130) {
			return "", errors.New("selection canceled")
		}
		return "", fmt.Errorf("run fzf: %w", err)
	}

	selection := strings.TrimSpace(out.String())
	if selection == "" {
		return "", errors.New("selection canceled")
	}
	return selection, nil
}

var runFZFPicker = func(rows []string, initialQuery string, header string) (string, error) {
	var input strings.Builder
	for _, row := range rows {
		input.WriteString(row)
		input.WriteByte('\n')
	}
	return runFZFWithInput(strings.NewReader(input.String()), initialQuery, header)
}

var runFZFPickerStreaming = func(initialRows []string, initialQuery string, header string, streamRows func(writeRow func(string) error) error) (string, error) {
	reader, writer := io.Pipe()
	go func() {
		defer writer.Close()
		writeRow := func(row string) error {
			if _, err := io.WriteString(writer, row); err != nil {
				return err
			}
			_, err := io.WriteString(writer, "\n")
			return err
		}

		for _, row := range initialRows {
			if err := writeRow(row); err != nil {
				return
			}
		}

		if streamRows == nil {
			return
		}
		_ = streamRows(func(row string) error {
			if err := writeRow(row); err != nil {
				if errors.Is(err, io.ErrClosedPipe) {
					return nil
				}
				return err
			}
			return nil
		})
	}()
	return runFZFWithInput(reader, initialQuery, header)
}

func resolveArgsWithPicker(args []string) ([]string, string, bool, error) {
	intent, err := parsePickerIntent(args)
	if err != nil {
		return nil, "", false, err
	}
	if !intent.needsPicker {
		return nil, "", false, nil
	}
	if !canUseInteractivePicker() {
		return nil, "", false, nil
	}

	identity, err := resolveKubeIdentityStruct(intent.baseArgs)
	if err != nil {
		return nil, "", true, err
	}

	historyEntries, err := loadHistoryEntries()
	if err != nil {
		historyEntries = map[string]historyEntry{}
	}
	aliasesByKey := map[string]preferredAlias{}
	if aliases, err := loadAliases(); err == nil {
		aliasesByKey = buildPreferredAliasesByHistoryKey(aliases)
	}
	savedCommands := buildHistoryCommands(historyEntries)
	savedCommands = enrichCommandsWithAliases(savedCommands, aliasesByKey)
	savedKeys := make(map[string]struct{}, len(savedCommands))
	for _, command := range savedCommands {
		savedKeys[command.HistoryKey] = struct{}{}
	}

	savedRows, err := commandRows(savedCommands)
	if err != nil {
		return nil, "", true, err
	}
	savedKeys = make(map[string]struct{}, len(savedCommands))
	for _, command := range savedCommands {
		savedKeys[command.HistoryKey] = struct{}{}
	}

	// If history exists, show it immediately and stream discovered options later.
	if len(savedRows) > 0 {
		header := pickerStreamingHeader(identity, len(savedRows))
		selection, err := runFZFPickerStreaming(savedRows, intent.initialQuery, header, func(writeRow func(string) error) error {
			candidates, err := discoverPortForwardCandidates(intent.selection)
			if err != nil {
				// Keep saved results usable even if live discovery fails.
				return nil
			}
			commands, err := buildResolvedCommands(intent.baseArgs, identity, candidates)
			if err != nil {
				return err
			}
			commands = enrichCommandsWithAliases(commands, aliasesByKey)
			return appendUniqueCommandRows(commands, savedKeys, writeRow)
		})
		if err != nil {
			return nil, "", true, err
		}
		resolvedSelection, err := decodePickerSelection(selection)
		if err != nil {
			return nil, "", true, err
		}
		return resolvedSelection.Args, resolvedSelection.PortName, true, nil
	}

	candidates, err := discoverPortForwardCandidates(intent.selection)
	if err != nil {
		return nil, "", true, err
	}
	if len(candidates) == 0 {
		return nil, "", true, errors.New("no port-forward candidates found in the selected context")
	}

	commands, err := buildResolvedCommands(intent.baseArgs, identity, candidates)
	if err != nil {
		return nil, "", true, err
	}
	commands = enrichCommandsWithAliases(commands, aliasesByKey)
	history, err := loadHistory()
	if err != nil {
		history = map[string]int64{}
	}
	commands = orderResolvedCommandsByHistory(commands, history)

	rows, err := commandRows(commands)
	if err != nil {
		return nil, "", true, err
	}

	header := pickerHeader(identity, len(candidates))
	selection, err := runFZFPicker(rows, intent.initialQuery, header)
	if err != nil {
		return nil, "", true, err
	}
	resolvedSelection, err := decodePickerSelection(selection)
	if err != nil {
		return nil, "", true, err
	}
	return resolvedSelection.Args, resolvedSelection.PortName, true, nil
}

func decodePickerSelection(selection string) (pickerSelection, error) {
	tab := strings.LastIndexByte(selection, '\t')
	if tab < 0 || tab == len(selection)-1 {
		return pickerSelection{}, errors.New("invalid picker response")
	}
	return decodePickerPayload(selection[tab+1:])
}

func commandRows(commands []resolvedCommand) ([]string, error) {
	rows := make([]string, 0, len(commands))
	for _, command := range commands {
		payload, err := encodePickerPayload(command.ResolvedArgs, command.PortName)
		if err != nil {
			return nil, err
		}
		rows = append(rows, colorizeResourceKinds(command.Display)+"\t"+payload)
	}
	return rows, nil
}

func appendUniqueCommandRows(commands []resolvedCommand, seen map[string]struct{}, writeRow func(string) error) error {
	for _, command := range commands {
		if _, ok := seen[command.HistoryKey]; ok {
			continue
		}
		seen[command.HistoryKey] = struct{}{}

		payload, err := encodePickerPayload(command.ResolvedArgs, command.PortName)
		if err != nil {
			return err
		}
		if err := writeRow(colorizeResourceKinds(command.Display) + "\t" + payload); err != nil {
			return err
		}
	}
	return nil
}

func buildHistoryCommands(entries map[string]historyEntry) []resolvedCommand {
	commands := make([]resolvedCommand, 0, len(entries))
	for key, entry := range entries {
		if len(entry.Args) == 0 {
			continue
		}
		args := append([]string(nil), entry.Args...)
		hKey := historyKey(args)
		if hKey == "" {
			hKey = key
		}
		commands = append(commands, resolvedCommand{
			ResolvedArgs: args,
			Display:      formatDisplayLine(args, entry.PortName),
			HistoryKey:   hKey,
			LastUsedUnix: entry.LastUsedUnix,
			PortName:     entry.PortName,
		})
	}
	sort.SliceStable(commands, func(i, j int) bool {
		left := commands[i]
		right := commands[j]
		if left.LastUsedUnix != right.LastUsedUnix {
			return left.LastUsedUnix > right.LastUsedUnix
		}
		return left.Display < right.Display
	})
	return commands
}

func buildPreferredAliasesByHistoryKey(aliases map[string]aliasEntry) map[string]preferredAlias {
	selected := make(map[string]preferredAlias)
	for name, entry := range aliases {
		if len(entry.Args) == 0 {
			continue
		}
		key := historyKey(entry.Args)
		if key == "" {
			continue
		}
		current, ok := selected[key]
		if !ok || entry.UpdatedAtUnix > current.Entry.UpdatedAtUnix || (entry.UpdatedAtUnix == current.Entry.UpdatedAtUnix && name < current.Name) {
			selected[key] = preferredAlias{
				Name: name,
				Entry: aliasEntry{
					Args:          append([]string(nil), entry.Args...),
					PortName:      entry.PortName,
					UpdatedAtUnix: entry.UpdatedAtUnix,
				},
			}
		}
	}
	return selected
}

func enrichCommandsWithAliases(commands []resolvedCommand, aliasesByKey map[string]preferredAlias) []resolvedCommand {
	if len(aliasesByKey) == 0 {
		return commands
	}
	enriched := append([]resolvedCommand(nil), commands...)
	for i := range enriched {
		cmd := enriched[i]
		alias, ok := aliasesByKey[cmd.HistoryKey]
		if !ok {
			continue
		}
		portName := cmd.PortName
		if portName == "" {
			portName = alias.Entry.PortName
		}
		enriched[i].PortName = portName
		enriched[i].Display = formatDisplayLineWithAlias(alias.Name, cmd.ResolvedArgs, portName)
	}
	return enriched
}

func buildResolvedCommands(baseArgs []string, identity resolvedKubeIdentity, candidates []portForwardCandidate) ([]resolvedCommand, error) {
	commands := make([]resolvedCommand, 0, len(candidates))
	for _, candidate := range candidates {
		resolvedArgs, err := buildResolvedPortForwardArgs(baseArgs, identity, candidate)
		if err != nil {
			return nil, err
		}
		commands = append(commands, resolvedCommand{
			ResolvedArgs: resolvedArgs,
			Display:      formatDisplayLine(resolvedArgs, candidate.PortName),
			HistoryKey:   historyKey(resolvedArgs),
			PortName:     candidate.PortName,
		})
	}
	return commands, nil
}

func parsePickerIntent(args []string) (pickerIntent, error) {
	intent := pickerIntent{}
	if len(args) > 0 && args[0] == "port-forward" {
		args = args[1:]
	}

	positionals := make([]string, 0, len(args))
	literalMode := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !literalMode && arg == "--" {
			literalMode = true
			continue
		}
		if !literalMode && strings.HasPrefix(arg, "-") {
			intent.baseArgs = append(intent.baseArgs, arg)
			if !flagConsumesValue(arg) || strings.Contains(arg, "=") {
				continue
			}
			if i+1 >= len(args) {
				return pickerIntent{}, fmt.Errorf("missing value for flag %q", arg)
			}
			intent.baseArgs = append(intent.baseArgs, args[i+1])
			i++
			continue
		}
		positionals = append(positionals, arg)
	}

	selection, err := extractKubeSelection(intent.baseArgs)
	if err != nil {
		return pickerIntent{}, err
	}
	intent.selection = selection

	if len(positionals) == 2 {
		if _, _, err := parsePortArg(positionals[1]); err == nil {
			return intent, nil
		}
	}

	intent.needsPicker = true
	intent.initialQuery = strings.Join(positionals, " ")
	return intent, nil
}

func discoverPortForwardCandidates(selection kubeSelection) ([]portForwardCandidate, error) {
	kubectlArgs := make([]string, 0, len(selection.configArgs)+8)
	kubectlArgs = append(kubectlArgs, selection.configArgs...)
	kubectlArgs = append(kubectlArgs, "get", kubectlDiscoveryResources)
	if selection.namespacePresent {
		kubectlArgs = append(kubectlArgs, "--namespace", selection.namespace)
	} else {
		kubectlArgs = append(kubectlArgs, "--all-namespaces")
	}
	kubectlArgs = append(kubectlArgs, "-o", "json")

	output, err := runKubectlCombinedOutput(kubectlArgs)
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if msg != "" {
			return nil, fmt.Errorf("discover port-forward candidates: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("discover port-forward candidates: %w", err)
	}
	return parsePortForwardCandidates(output)
}

func parsePortForwardCandidates(raw []byte) ([]portForwardCandidate, error) {
	var list kubectlResourceList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decode candidate list: %w", err)
	}

	var candidates []portForwardCandidate
	candidateIndexByKey := make(map[string]int)
	addCandidate := func(namespace, resource, remotePort, portName string) {
		if namespace == "" || resource == "" || remotePort == "" {
			return
		}
		key := namespace + "\x00" + resource + "\x00" + remotePort
		if idx, ok := candidateIndexByKey[key]; ok {
			candidates[idx].PortName = mergePortNames(candidates[idx].PortName, portName)
			return
		}
		candidateIndexByKey[key] = len(candidates)
		candidates = append(candidates, portForwardCandidate{
			Namespace:  namespace,
			Resource:   resource,
			RemotePort: remotePort,
			PortName:   portName,
		})
	}

	for _, item := range list.Items {
		switch strings.ToLower(item.Kind) {
		case "service":
			var spec kubectlServiceSpec
			if err := json.Unmarshal(item.Spec, &spec); err != nil {
				continue
			}
			for _, port := range spec.Ports {
				if port.Port > 0 {
					addCandidate(item.Metadata.Namespace, "svc/"+item.Metadata.Name, strconv.Itoa(port.Port), port.Name)
				}
			}
		case "pod":
			var spec kubectlPodSpec
			if err := json.Unmarshal(item.Spec, &spec); err != nil {
				continue
			}
			for _, container := range spec.Containers {
				for _, port := range container.Ports {
					if port.ContainerPort > 0 {
						addCandidate(item.Metadata.Namespace, "pod/"+item.Metadata.Name, strconv.Itoa(port.ContainerPort), port.Name)
					}
				}
			}
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
		if left.Namespace != right.Namespace {
			return left.Namespace < right.Namespace
		}
		if left.Resource != right.Resource {
			return left.Resource < right.Resource
		}
		leftPort, leftErr := strconv.Atoi(left.RemotePort)
		rightPort, rightErr := strconv.Atoi(right.RemotePort)
		if leftErr == nil && rightErr == nil && leftPort != rightPort {
			return leftPort < rightPort
		}
		return left.RemotePort < right.RemotePort
	})

	return candidates, nil
}

func orderResolvedCommandsByHistory(commands []resolvedCommand, history map[string]int64) []resolvedCommand {
	ordered := append([]resolvedCommand(nil), commands...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left := ordered[i]
		right := ordered[j]

		leftLastUsed, leftInHistory := history[left.HistoryKey]
		rightLastUsed, rightInHistory := history[right.HistoryKey]
		if leftInHistory != rightInHistory {
			return leftInHistory
		}
		if leftInHistory && rightInHistory && leftLastUsed != rightLastUsed {
			return leftLastUsed > rightLastUsed
		}
		return false
	})
	return ordered
}

func resolveKubeIdentityStruct(args []string) (resolvedKubeIdentity, error) {
	encoded, err := resolveKubeIdentity(args)
	if err != nil {
		return resolvedKubeIdentity{}, err
	}
	var identity resolvedKubeIdentity
	if err := json.Unmarshal([]byte(encoded), &identity); err != nil {
		return resolvedKubeIdentity{}, fmt.Errorf("decode kube identity: %w", err)
	}
	return identity, nil
}

func buildResolvedPortForwardArgs(baseArgs []string, identity resolvedKubeIdentity, candidate portForwardCandidate) ([]string, error) {
	cleaned, err := stripSelectionFlags(baseArgs)
	if err != nil {
		return nil, err
	}

	resolved := append([]string(nil), cleaned...)
	if identity.Context != "" {
		resolved = append(resolved, "--context="+identity.Context)
	}
	if identity.Cluster != "" {
		resolved = append(resolved, "--cluster="+identity.Cluster)
	}
	resolved = append(resolved, "--namespace="+candidate.Namespace, candidate.Resource, ":"+candidate.RemotePort)
	return resolved, nil
}

func displayArgs(args []string) []string {
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--context" || arg == "--cluster":
			if i+1 < len(args) {
				i++
			}
			continue
		case strings.HasPrefix(arg, "--context="), strings.HasPrefix(arg, "--cluster="):
			continue
		case arg == "--namespace":
			filtered = append(filtered, "-n")
			if i+1 < len(args) {
				filtered = append(filtered, args[i+1])
				i++
			}
			continue
		case strings.HasPrefix(arg, "--namespace="):
			filtered = append(filtered, "-n", strings.TrimPrefix(arg, "--namespace="))
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

func formatDisplayLine(args []string, portName string) string {
	line := strings.Join(displayArgs(args), " ")
	if portName == "" {
		return line
	}
	return line + " # " + portName
}

func formatDisplayLineWithAlias(aliasName string, args []string, portName string) string {
	line := formatDisplayLine(args, portName)
	if aliasName == "" {
		return line
	}
	return line + " (" + aliasName + ")"
}

func colorizeResourceKinds(display string) string {
	base, aliasSuffix := splitAliasSuffix(display)
	fields := strings.Fields(base)
	for i, field := range fields {
		switch {
		case strings.HasPrefix(field, "pod/"):
			fields[i] = colorizeResourceField(field, "pod/", "36")
		case strings.HasPrefix(field, "svc/"):
			fields[i] = colorizeResourceField(field, "svc/", "33")
		}
	}
	colored := strings.Join(fields, " ")
	if idx := strings.Index(colored, " # "); idx >= 0 {
		colored = colored[:idx+1] + "\x1b[90m" + colored[idx+1:] + "\x1b[0m"
	}
	if aliasSuffix != "" {
		colored += " " + "\x1b[95m" + aliasSuffix + "\x1b[0m"
	}
	return colored
}

func colorizeResourceField(field string, prefix string, colorCode string) string {
	name := strings.TrimPrefix(field, prefix)
	if name == "" {
		return "\x1b[" + colorCode + "m" + field + "\x1b[0m"
	}
	return "\x1b[" + colorCode + "m" + prefix + "\x1b[1;" + colorCode + "m" + name + "\x1b[0m"
}

func splitAliasSuffix(display string) (string, string) {
	start := strings.LastIndex(display, " (")
	if start < 0 || !strings.HasSuffix(display, ")") {
		return display, ""
	}
	alias := display[start+1:]
	if len(alias) <= 2 {
		return display, ""
	}
	aliasBody := alias[1 : len(alias)-1]
	if strings.ContainsAny(aliasBody, " \t") {
		return display, ""
	}
	return display[:start], alias
}

func historyKey(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return requestKey(args)
}

func mergePortNames(existing, next string) string {
	if existing == "" {
		return next
	}
	if next == "" || next == existing {
		return existing
	}

	seen := map[string]struct{}{}
	names := strings.Split(existing, ",")
	for _, name := range names {
		if name == "" {
			continue
		}
		seen[name] = struct{}{}
	}
	if _, ok := seen[next]; ok {
		return existing
	}
	names = append(names, next)
	sort.Strings(names)
	return strings.Join(names, ",")
}

func stripSelectionFlags(args []string) ([]string, error) {
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--context" || arg == "--cluster" || arg == "--namespace" || arg == "-n":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("missing value for flag %q", arg)
			}
			i++
			continue
		case strings.HasPrefix(arg, "--context="),
			strings.HasPrefix(arg, "--cluster="),
			strings.HasPrefix(arg, "--namespace="),
			strings.HasPrefix(arg, "-n="):
			continue
		}

		filtered = append(filtered, arg)
		if !flagConsumesValue(arg) || strings.Contains(arg, "=") {
			continue
		}
		if i+1 >= len(args) {
			return nil, fmt.Errorf("missing value for flag %q", arg)
		}
		filtered = append(filtered, args[i+1])
		i++
	}
	return filtered, nil
}

func flagConsumesValue(arg string) bool {
	if arg == "--ttl" {
		return true
	}
	return flagTakesValue(arg)
}

func encodePickerPayload(args []string, portName string) (string, error) {
	raw, err := json.Marshal(pickerSelection{
		Args:     append([]string(nil), args...),
		PortName: portName,
	})
	if err != nil {
		return "", fmt.Errorf("encode picker payload: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func decodePickerPayload(payload string) (pickerSelection, error) {
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return pickerSelection{}, fmt.Errorf("decode picker payload: %w", err)
	}

	var selection pickerSelection
	if err := json.Unmarshal(raw, &selection); err == nil && selection.Args != nil {
		return selection, nil
	}

	// Backward compatibility for old payloads encoded as just []string.
	var args []string
	if err := json.Unmarshal(raw, &args); err != nil {
		return pickerSelection{}, fmt.Errorf("decode picker payload: %w", err)
	}
	return pickerSelection{Args: args}, nil
}

func pickerHeader(identity resolvedKubeIdentity, candidateCount int) string {
	parts := make([]string, 0, 4)
	if identity.Context != "" {
		parts = append(parts, "context="+identity.Context)
	}
	if identity.Cluster != "" {
		parts = append(parts, "cluster="+identity.Cluster)
	}
	parts = append(parts, fmt.Sprintf("%d candidates", candidateCount))
	parts = append(parts, "enter=select esc=cancel")
	return strings.Join(parts, " | ")
}

func pickerStreamingHeader(identity resolvedKubeIdentity, savedCount int) string {
	parts := make([]string, 0, 5)
	if identity.Context != "" {
		parts = append(parts, "context="+identity.Context)
	}
	if identity.Cluster != "" {
		parts = append(parts, "cluster="+identity.Cluster)
	}
	parts = append(parts, fmt.Sprintf("%d saved results", savedCount))
	parts = append(parts, "discovering live candidates...")
	parts = append(parts, "enter=select esc=cancel")
	return strings.Join(parts, " | ")
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
