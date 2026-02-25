package client

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bduffany/kpf/internal/protocol"
)

func TestExtractKubeSelection(t *testing.T) {
	t.Parallel()

	selection, err := extractKubeSelection([]string{
		"--context=ctx-a",
		"--cluster", "cluster-a",
		"--kubeconfig=/tmp/kubeconfig",
		"-n", "ns-a",
		"pod/mypod",
		"8080",
		"--",
		"--context", "ignored",
	})
	if err != nil {
		t.Fatalf("extractKubeSelection() error = %v", err)
	}

	wantConfigArgs := []string{
		"--context=ctx-a",
		"--cluster", "cluster-a",
		"--kubeconfig=/tmp/kubeconfig",
	}
	if len(selection.configArgs) != len(wantConfigArgs) {
		t.Fatalf("config arg count = %d, want %d", len(selection.configArgs), len(wantConfigArgs))
	}
	for i, want := range wantConfigArgs {
		if got := selection.configArgs[i]; got != want {
			t.Fatalf("config arg[%d] = %q, want %q", i, got, want)
		}
	}
	if !selection.namespacePresent {
		t.Fatalf("namespacePresent = false, want true")
	}
	if got := selection.namespace; got != "ns-a" {
		t.Fatalf("namespace = %q, want %q", got, "ns-a")
	}
}

func TestExtractKubeSelectionMissingValue(t *testing.T) {
	t.Parallel()

	_, err := extractKubeSelection([]string{"--context"})
	if err == nil {
		t.Fatalf("expected missing value error")
	}
}

func TestCanonicalizeKubeIdentity(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
  "current-context":"dev",
  "contexts":[
    {"name":"dev","context":{"cluster":"cluster-dev","user":"user-dev","namespace":"team-a"}},
    {"name":"staging","context":{"cluster":"cluster-staging","user":"user-staging","namespace":"team-b"}}
  ],
  "clusters":[
    {"name":"cluster-dev","cluster":{"server":"https://dev.example.test"}},
    {"name":"cluster-staging","cluster":{"server":"https://staging.example.test"}}
  ]
}`)

	got, err := canonicalizeKubeIdentity(raw, kubeSelection{})
	if err != nil {
		t.Fatalf("canonicalizeKubeIdentity() error = %v", err)
	}

	var identity resolvedKubeIdentity
	if err := json.Unmarshal([]byte(got), &identity); err != nil {
		t.Fatalf("unmarshal identity: %v", err)
	}

	if identity.Context != "dev" {
		t.Fatalf("context = %q, want %q", identity.Context, "dev")
	}
	if identity.Cluster != "cluster-dev" {
		t.Fatalf("cluster = %q, want %q", identity.Cluster, "cluster-dev")
	}
	if identity.Server != "https://dev.example.test" {
		t.Fatalf("server = %q, want %q", identity.Server, "https://dev.example.test")
	}
	if identity.User != "user-dev" {
		t.Fatalf("user = %q, want %q", identity.User, "user-dev")
	}
	if identity.Namespace != "team-a" {
		t.Fatalf("namespace = %q, want %q", identity.Namespace, "team-a")
	}
}

func TestCanonicalizeKubeIdentityNamespaceOverride(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
  "contexts":[{"name":"dev","context":{"cluster":"cluster-dev","user":"user-dev"}}],
  "clusters":[{"name":"cluster-dev","cluster":{"server":"https://dev.example.test"}}]
}`)

	selection := kubeSelection{namespacePresent: true, namespace: "override-ns"}
	got, err := canonicalizeKubeIdentity(raw, selection)
	if err != nil {
		t.Fatalf("canonicalizeKubeIdentity() error = %v", err)
	}

	var identity resolvedKubeIdentity
	if err := json.Unmarshal([]byte(got), &identity); err != nil {
		t.Fatalf("unmarshal identity: %v", err)
	}

	if identity.Namespace != "override-ns" {
		t.Fatalf("namespace = %q, want %q", identity.Namespace, "override-ns")
	}
}

func TestExtractTTLEnvVar(t *testing.T) {
	t.Setenv(ttlEnvVar, "45m")

	filtered, ttl, err := extractTTL([]string{"svc/api", "8080"})
	if err != nil {
		t.Fatalf("extractTTL() error = %v", err)
	}
	if got, want := ttl, 45*time.Minute; got != want {
		t.Fatalf("ttl = %v, want %v", got, want)
	}
	if len(filtered) != 2 || filtered[0] != "svc/api" || filtered[1] != "8080" {
		t.Fatalf("filtered args = %v, want [svc/api 8080]", filtered)
	}
}

func TestExtractTTLFlagOverridesEnvVar(t *testing.T) {
	t.Setenv(ttlEnvVar, "45m")

	_, ttl, err := extractTTL([]string{"--ttl", "10m", "svc/api", "8080"})
	if err != nil {
		t.Fatalf("extractTTL() error = %v", err)
	}
	if got, want := ttl, 10*time.Minute; got != want {
		t.Fatalf("ttl = %v, want %v", got, want)
	}
}

func TestExtractTTLInvalidEnvVar(t *testing.T) {
	t.Setenv(ttlEnvVar, "bad")

	_, _, err := extractTTL([]string{"svc/api", "8080"})
	if err == nil {
		t.Fatalf("expected error for invalid %s", ttlEnvVar)
	}
	if !strings.Contains(err.Error(), ttlEnvVar) {
		t.Fatalf("error = %q, want mention of %s", err.Error(), ttlEnvVar)
	}
}

func TestExtractTTLDefaultsWhenUnset(t *testing.T) {
	_, ttl, err := extractTTL([]string{"svc/api", "8080"})
	if err != nil {
		t.Fatalf("extractTTL() error = %v", err)
	}
	if got, want := ttl, protocol.DefaultSessionTTL; got != want {
		t.Fatalf("ttl = %v, want %v", got, want)
	}
}

func TestExtractForeground(t *testing.T) {
	filtered, foreground := extractForeground([]string{"--fg", "--namespace", "team-a", "svc/api", "8080"})
	if !foreground {
		t.Fatalf("foreground = false, want true")
	}
	want := []string{"--namespace", "team-a", "svc/api", "8080"}
	if !reflect.DeepEqual(filtered, want) {
		t.Fatalf("filtered = %v, want %v", filtered, want)
	}
}

func TestExtractAliasActionSet(t *testing.T) {
	filtered, action, name, err := extractAliasAction([]string{"-a", "vmselect", "--namespace", "team-a", "svc/api"})
	if err != nil {
		t.Fatalf("extractAliasAction() error = %v", err)
	}
	if action != aliasActionSet {
		t.Fatalf("action = %v, want %v", action, aliasActionSet)
	}
	if got, want := name, "vmselect"; got != want {
		t.Fatalf("name = %q, want %q", got, want)
	}
	want := []string{"--namespace", "team-a", "svc/api"}
	if !reflect.DeepEqual(filtered, want) {
		t.Fatalf("filtered = %v, want %v", filtered, want)
	}
}

func TestExtractAliasActionSetEqualsForm(t *testing.T) {
	filtered, action, name, err := extractAliasAction([]string{"--alias=vmselect", "svc/api"})
	if err != nil {
		t.Fatalf("extractAliasAction() error = %v", err)
	}
	if action != aliasActionSet {
		t.Fatalf("action = %v, want %v", action, aliasActionSet)
	}
	if got, want := name, "vmselect"; got != want {
		t.Fatalf("name = %q, want %q", got, want)
	}
	want := []string{"svc/api"}
	if !reflect.DeepEqual(filtered, want) {
		t.Fatalf("filtered = %v, want %v", filtered, want)
	}
}

func TestExtractAliasActionMissingValue(t *testing.T) {
	_, _, _, err := extractAliasAction([]string{"-a", "--namespace=team-a"})
	if err == nil {
		t.Fatalf("expected error")
	}
	if got, want := err.Error(), "missing value for flag \"-a\""; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestExtractForegroundShortFlag(t *testing.T) {
	filtered, foreground := extractForeground([]string{"-f", "--namespace", "team-a", "svc/api", "8080"})
	if !foreground {
		t.Fatalf("foreground = false, want true")
	}
	want := []string{"--namespace", "team-a", "svc/api", "8080"}
	if !reflect.DeepEqual(filtered, want) {
		t.Fatalf("filtered = %v, want %v", filtered, want)
	}
}

func TestExtractList(t *testing.T) {
	filtered, list := extractList([]string{"--list", "--namespace", "team-a", "svc/api", "8080"})
	if !list {
		t.Fatalf("list = false, want true")
	}
	want := []string{"--namespace", "team-a", "svc/api", "8080"}
	if !reflect.DeepEqual(filtered, want) {
		t.Fatalf("filtered = %v, want %v", filtered, want)
	}
}

func TestExtractListLiteralMode(t *testing.T) {
	filtered, list := extractList([]string{"--", "--list", "svc/api", "8080"})
	if list {
		t.Fatalf("list = true, want false")
	}
	want := []string{"--", "--list", "svc/api", "8080"}
	if !reflect.DeepEqual(filtered, want) {
		t.Fatalf("filtered = %v, want %v", filtered, want)
	}
}

func TestExtractForegroundLiteralMode(t *testing.T) {
	filtered, foreground := extractForeground([]string{"--", "--fg", "-f", "svc/api", "8080"})
	if foreground {
		t.Fatalf("foreground = true, want false")
	}
	want := []string{"--", "--fg", "-f", "svc/api", "8080"}
	if !reflect.DeepEqual(filtered, want) {
		t.Fatalf("filtered = %v, want %v", filtered, want)
	}
}

func TestFormatHelpOutput(t *testing.T) {
	raw := "Usage:\n  kubectl port-forward pod/mypod 8080\n"
	got := formatHelpOutput(raw)
	if strings.Contains(got, "Usage:\n  kubectl port-forward ") {
		t.Fatalf("got help still contains kubectl usage command: %q", got)
	}
	if !strings.Contains(got, "Usage:\n  kpf ") {
		t.Fatalf("got help missing rewritten kpf usage command: %q", got)
	}
	if !strings.Contains(got, "kpf options:") {
		t.Fatalf("got help missing kpf options section: %q", got)
	}
	if !strings.Contains(got, "--alias, -a NAME:") {
		t.Fatalf("got help missing --alias option: %q", got)
	}
	if !strings.Contains(got, "--list:") {
		t.Fatalf("got help missing --list: %q", got)
	}
	if !strings.Contains(got, "--fg, -f:") {
		t.Fatalf("got help missing --fg, -f: %q", got)
	}
	wantTTLFlag := "--ttl=" + protocol.DefaultSessionTTL.String() + ":"
	if !strings.Contains(got, wantTTLFlag) {
		t.Fatalf("got help missing ttl option %q: %q", wantTTLFlag, got)
	}
	if !strings.Contains(got, ttlEnvVar) {
		t.Fatalf("got help missing env var %q: %q", ttlEnvVar, got)
	}
}

func TestUsageWithFZFInstallTipError(t *testing.T) {
	err := usageWithFZFInstallTipError()
	if err == nil {
		t.Fatalf("usageWithFZFInstallTipError() = nil, want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, helpUsage) {
		t.Fatalf("error = %q, want help usage", msg)
	}
	if !strings.Contains(msg, "TIP: install fzf to get search: "+fzfInstallURL) {
		t.Fatalf("error = %q, want fzf install tip", msg)
	}
}

func TestMapPickerErrorFZFUnavailable(t *testing.T) {
	err := mapPickerError(errFZFUnavailable)
	if err == nil {
		t.Fatalf("mapPickerError() = nil, want error")
	}
	if !strings.Contains(err.Error(), "TIP: install fzf to get search: "+fzfInstallURL) {
		t.Fatalf("error = %q, want fzf install tip", err.Error())
	}
}
