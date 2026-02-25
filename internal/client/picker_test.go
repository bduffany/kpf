package client

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveArgsWithPickerSkipsWhenNotInteractive(t *testing.T) {
	originalCanUseInteractivePicker := canUseInteractivePicker
	canUseInteractivePicker = func() bool { return false }
	t.Cleanup(func() {
		canUseInteractivePicker = originalCanUseInteractivePicker
	})

	args, portName, usedPicker, err := resolveArgsWithPicker(nil)
	if err != nil {
		t.Fatalf("resolveArgsWithPicker() error = %v", err)
	}
	if usedPicker {
		t.Fatalf("usedPicker = true, want false")
	}
	if args != nil {
		t.Fatalf("args = %v, want nil", args)
	}
	if portName != "" {
		t.Fatalf("portName = %q, want empty", portName)
	}
}

func TestParsePickerIntentNeedsPickerForNoArgs(t *testing.T) {
	t.Parallel()

	intent, err := parsePickerIntent(nil)
	if err != nil {
		t.Fatalf("parsePickerIntent() error = %v", err)
	}
	if !intent.needsPicker {
		t.Fatalf("needsPicker = false, want true")
	}
	if intent.initialQuery != "" {
		t.Fatalf("initialQuery = %q, want empty", intent.initialQuery)
	}
}

func TestParsePickerIntentResolvedTargetSkipsPicker(t *testing.T) {
	t.Parallel()

	intent, err := parsePickerIntent([]string{
		"--context=dev",
		"--namespace=team-a",
		"svc/api",
		"8080",
	})
	if err != nil {
		t.Fatalf("parsePickerIntent() error = %v", err)
	}
	if intent.needsPicker {
		t.Fatalf("needsPicker = true, want false")
	}
}

func TestParsePickerIntentPartialCollectsQueryAndFlags(t *testing.T) {
	t.Parallel()

	intent, err := parsePickerIntent([]string{
		"--ttl", "15m",
		"--namespace", "team-a",
		"svc/api",
	})
	if err != nil {
		t.Fatalf("parsePickerIntent() error = %v", err)
	}
	if !intent.needsPicker {
		t.Fatalf("needsPicker = false, want true")
	}
	if got, want := intent.initialQuery, "svc/api"; got != want {
		t.Fatalf("initialQuery = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(intent.baseArgs, []string{"--ttl", "15m", "--namespace", "team-a"}) {
		t.Fatalf("baseArgs = %v, want %v", intent.baseArgs, []string{"--ttl", "15m", "--namespace", "team-a"})
	}
}

func TestParsePortForwardCandidates(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
  "items": [
    {
      "kind": "Service",
      "metadata": {"name": "api", "namespace": "team-a"},
      "spec": {"ports": [{"name":"http","port": 8080}, {"name":"admin","port": 9090}]}
    },
    {
      "kind": "Pod",
      "metadata": {"name": "api-123", "namespace": "team-a"},
      "spec": {
        "containers": [
          {"ports": [{"name":"web","containerPort": 8080}, {"name":"metrics","containerPort": 7070}]},
          {"ports": [{"name":"metrics","containerPort": 7070}]}
        ]
      }
    }
  ]
}`)

	candidates, err := parsePortForwardCandidates(raw)
	if err != nil {
		t.Fatalf("parsePortForwardCandidates() error = %v", err)
	}

	want := []portForwardCandidate{
		{Namespace: "team-a", Resource: "pod/api-123", RemotePort: "7070", PortName: "metrics"},
		{Namespace: "team-a", Resource: "pod/api-123", RemotePort: "8080", PortName: "web"},
		{Namespace: "team-a", Resource: "svc/api", RemotePort: "8080", PortName: "http"},
		{Namespace: "team-a", Resource: "svc/api", RemotePort: "9090", PortName: "admin"},
	}
	if !reflect.DeepEqual(candidates, want) {
		t.Fatalf("candidates = %v, want %v", candidates, want)
	}
}

func TestBuildResolvedPortForwardArgs(t *testing.T) {
	t.Parallel()

	args, err := buildResolvedPortForwardArgs(
		[]string{"--ttl", "10m", "--namespace=old-ns", "--context=old-ctx", "--cluster=old-cluster"},
		resolvedKubeIdentity{Context: "ctx-a", Cluster: "cluster-a"},
		portForwardCandidate{Namespace: "team-a", Resource: "svc/api", RemotePort: "8080"},
	)
	if err != nil {
		t.Fatalf("buildResolvedPortForwardArgs() error = %v", err)
	}

	want := []string{
		"--ttl", "10m",
		"--context=ctx-a",
		"--cluster=cluster-a",
		"--namespace=team-a",
		"svc/api",
		":8080",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestDisplayArgsHidesContextAndCluster(t *testing.T) {
	t.Parallel()

	got := displayArgs([]string{
		"--ttl", "10m",
		"--context=ctx-a",
		"--cluster=cluster-a",
		"--namespace=team-a",
		"svc/api",
		":8080",
	})
	want := []string{
		"--ttl", "10m",
		"-n", "team-a",
		"svc/api",
		":8080",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("displayArgs() = %v, want %v", got, want)
	}
}

func TestFormatDisplayLineIncludesPortNameComment(t *testing.T) {
	t.Parallel()

	got := formatDisplayLine([]string{"-n", "team-a", "svc/api", ":8080"}, "http")
	want := "-n team-a svc/api :8080 # http"
	if got != want {
		t.Fatalf("formatDisplayLine() = %q, want %q", got, want)
	}
}

func TestColorizeResourceKinds(t *testing.T) {
	t.Parallel()

	got := colorizeResourceKinds("-n monitor-prod svc/victoria :8481 # http (vmselect)")
	want := "-n monitor-prod \x1b[33msvc/\x1b[1;33mvictoria\x1b[0m :8481 \x1b[90m# http\x1b[0m \x1b[95m(vmselect)\x1b[0m"
	if got != want {
		t.Fatalf("colorizeResourceKinds() = %q, want %q", got, want)
	}

	got = colorizeResourceKinds("-n team-a pod/api-123 :8080 # web")
	want = "-n team-a \x1b[36mpod/\x1b[1;36mapi-123\x1b[0m :8080 \x1b[90m# web\x1b[0m"
	if got != want {
		t.Fatalf("colorizeResourceKinds() = %q, want %q", got, want)
	}

	got = colorizeResourceKinds("-n team-a pod/api-123 :8080")
	want = "-n team-a \x1b[36mpod/\x1b[1;36mapi-123\x1b[0m :8080"
	if got != want {
		t.Fatalf("colorizeResourceKinds() = %q, want %q", got, want)
	}
}

func TestBuildPreferredAliasesByHistoryKey(t *testing.T) {
	t.Parallel()

	apiArgs := []string{"--namespace=team-a", "svc/api", ":8080"}
	got := buildPreferredAliasesByHistoryKey(map[string]aliasEntry{
		"old-name": {
			Args:          apiArgs,
			PortName:      "http",
			UpdatedAtUnix: 100,
		},
		"new-name": {
			Args:          apiArgs,
			PortName:      "admin",
			UpdatedAtUnix: 200,
		},
	})
	key := historyKey(apiArgs)
	entry, ok := got[key]
	if !ok {
		t.Fatalf("missing key %q", key)
	}
	if gotName, want := entry.Name, "new-name"; gotName != want {
		t.Fatalf("entry.Name = %q, want %q", gotName, want)
	}
	if gotPort, want := entry.Entry.PortName, "admin"; gotPort != want {
		t.Fatalf("entry.PortName = %q, want %q", gotPort, want)
	}
}

func TestEnrichCommandsWithAliases(t *testing.T) {
	t.Parallel()

	args := []string{"--namespace=team-a", "svc/api", ":8080"}
	key := historyKey(args)
	commands := []resolvedCommand{
		{
			ResolvedArgs: args,
			Display:      "-n team-a svc/api :8080",
			HistoryKey:   key,
			PortName:     "",
		},
	}
	aliasesByKey := map[string]preferredAlias{
		key: {
			Name: "vmselect",
			Entry: aliasEntry{
				Args:     args,
				PortName: "http",
			},
		},
	}
	enriched := enrichCommandsWithAliases(commands, aliasesByKey)
	if len(enriched) != 1 {
		t.Fatalf("len(enriched) = %d, want 1", len(enriched))
	}
	if got, want := enriched[0].Display, "-n team-a svc/api :8080 # http (vmselect)"; got != want {
		t.Fatalf("display = %q, want %q", got, want)
	}
	if got, want := enriched[0].PortName, "http"; got != want {
		t.Fatalf("portName = %q, want %q", got, want)
	}
}

func TestMergePortNames(t *testing.T) {
	t.Parallel()

	if got := mergePortNames("", "http"); got != "http" {
		t.Fatalf("mergePortNames(empty, http) = %q, want %q", got, "http")
	}
	if got := mergePortNames("metrics", "metrics"); got != "metrics" {
		t.Fatalf("mergePortNames(metrics, metrics) = %q, want %q", got, "metrics")
	}
	if got := mergePortNames("metrics", "http"); got != "http,metrics" {
		t.Fatalf("mergePortNames(metrics, http) = %q, want %q", got, "http,metrics")
	}
}

func TestOrderResolvedCommandsByHistory(t *testing.T) {
	t.Parallel()

	commands := []resolvedCommand{
		{Display: "cmd-1", HistoryKey: "a"},
		{Display: "cmd-2", HistoryKey: "b"},
		{Display: "cmd-3", HistoryKey: "c"},
	}
	history := map[string]int64{
		"c": 300,
		"a": 100,
	}

	ordered := orderResolvedCommandsByHistory(commands, history)
	got := []string{ordered[0].Display, ordered[1].Display, ordered[2].Display}
	want := []string{"cmd-3", "cmd-1", "cmd-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("orderResolvedCommandsByHistory() = %v, want %v", got, want)
	}
}

func TestBuildHistoryCommandsOrdersByRecencyAndSkipsLegacyEntries(t *testing.T) {
	t.Parallel()

	commands := buildHistoryCommands(map[string]historyEntry{
		"legacy": {LastUsedUnix: 999},
		"older": {
			LastUsedUnix: 100,
			Args:         []string{"--namespace=team-a", "svc/alpha", ":8080"},
			PortName:     "admin",
		},
		"newer": {
			LastUsedUnix: 200,
			Args:         []string{"--namespace=team-a", "svc/beta", ":8080"},
			PortName:     "metrics",
		},
	})
	if len(commands) != 2 {
		t.Fatalf("len(commands) = %d, want %d", len(commands), 2)
	}
	if got, want := commands[0].Display, "-n team-a svc/beta :8080 # metrics"; got != want {
		t.Fatalf("commands[0].Display = %q, want %q", got, want)
	}
	if got, want := commands[1].Display, "-n team-a svc/alpha :8080 # admin"; got != want {
		t.Fatalf("commands[1].Display = %q, want %q", got, want)
	}
}

func TestAppendUniqueCommandRowsSkipsSeenRows(t *testing.T) {
	t.Parallel()

	commands := []resolvedCommand{
		{ResolvedArgs: []string{"--namespace=team-a", "svc/api", ":8080"}, Display: "-n team-a svc/api :8080", HistoryKey: "seen"},
		{ResolvedArgs: []string{"--namespace=team-a", "svc/metrics", ":9090"}, Display: "-n team-a svc/metrics :9090 # metrics", HistoryKey: "new", PortName: "metrics"},
	}
	seen := map[string]struct{}{"seen": {}}
	var rows []string
	if err := appendUniqueCommandRows(commands, seen, func(row string) error {
		rows = append(rows, row)
		return nil
	}); err != nil {
		t.Fatalf("appendUniqueCommandRows() error = %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want %d", len(rows), 1)
	}
	if _, ok := seen["new"]; !ok {
		t.Fatalf("seen missing key %q", "new")
	}
	parts := strings.Split(rows[0], "\t")
	if len(parts) != 2 {
		t.Fatalf("row format = %q, want display\\tpayload", rows[0])
	}
	selection, err := decodePickerPayload(parts[1])
	if err != nil {
		t.Fatalf("decodePickerPayload() error = %v", err)
	}
	want := []string{"--namespace=team-a", "svc/metrics", ":9090"}
	if !reflect.DeepEqual(selection.Args, want) {
		t.Fatalf("decoded args = %v, want %v", selection.Args, want)
	}
	if got, want := selection.PortName, "metrics"; got != want {
		t.Fatalf("decoded portName = %q, want %q", got, want)
	}
}
