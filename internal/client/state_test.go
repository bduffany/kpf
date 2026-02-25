package client

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStateDirUsesUserConfigDir(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	got := stateDir()
	want := filepath.Join(configHome, clientStateDirName)
	if got != want {
		t.Fatalf("stateDir() = %q, want %q", got, want)
	}
}

func TestRecordHistoryAndLoadHistory(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	args := []string{"--context=ctx-a", "--cluster=cluster-a", "--namespace=team-a", "svc/api", ":8080"}
	if err := recordHistory(args, "http"); err != nil {
		t.Fatalf("recordHistory() error = %v", err)
	}

	history, err := loadHistory()
	if err != nil {
		t.Fatalf("loadHistory() error = %v", err)
	}
	key := historyKey(args)
	if key == "" {
		t.Fatalf("historyKey() returned empty key")
	}
	if _, ok := history[key]; !ok {
		t.Fatalf("history key %q not found", key)
	}
}

func TestRecordHistoryPersistsArgs(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	args := []string{"--context=ctx-a", "--cluster=cluster-a", "--namespace=team-a", "svc/api", ":8080"}
	if err := recordHistory(args, "http"); err != nil {
		t.Fatalf("recordHistory() error = %v", err)
	}

	entries, err := loadHistoryEntries()
	if err != nil {
		t.Fatalf("loadHistoryEntries() error = %v", err)
	}
	key := historyKey(args)
	entry, ok := entries[key]
	if !ok {
		t.Fatalf("history entry %q not found", key)
	}
	if entry.LastUsedUnix <= 0 {
		t.Fatalf("entry.LastUsedUnix = %d, want > 0", entry.LastUsedUnix)
	}
	if !reflect.DeepEqual(entry.Args, args) {
		t.Fatalf("entry.Args = %v, want %v", entry.Args, args)
	}
	if got, want := entry.PortName, "http"; got != want {
		t.Fatalf("entry.PortName = %q, want %q", got, want)
	}
}

func TestLoadHistoryEntriesLegacyFormat(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	if err := writeJSONFile(historyPath(), map[string]any{
		"last_used_by_key": map[string]int64{
			"legacy-key": 123,
		},
	}); err != nil {
		t.Fatalf("writeJSONFile() error = %v", err)
	}

	entries, err := loadHistoryEntries()
	if err != nil {
		t.Fatalf("loadHistoryEntries() error = %v", err)
	}
	entry, ok := entries["legacy-key"]
	if !ok {
		t.Fatalf("legacy history key not found")
	}
	if entry.LastUsedUnix != 123 {
		t.Fatalf("entry.LastUsedUnix = %d, want %d", entry.LastUsedUnix, 123)
	}
	if len(entry.Args) != 0 {
		t.Fatalf("entry.Args = %v, want empty", entry.Args)
	}
	if entry.PortName != "" {
		t.Fatalf("entry.PortName = %q, want empty", entry.PortName)
	}
}

func TestRecordHistoryPreservesExistingPortNameWhenUnknown(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	args := []string{"--namespace=team-a", "svc/api", ":8080"}
	if err := recordHistory(args, "http"); err != nil {
		t.Fatalf("recordHistory() first write error = %v", err)
	}
	if err := recordHistory(args, ""); err != nil {
		t.Fatalf("recordHistory() second write error = %v", err)
	}

	entries, err := loadHistoryEntries()
	if err != nil {
		t.Fatalf("loadHistoryEntries() error = %v", err)
	}
	key := historyKey(args)
	if got, want := entries[key].PortName, "http"; got != want {
		t.Fatalf("entry.PortName = %q, want %q", got, want)
	}
}

func TestSaveAliasAndLoadAliases(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	args := []string{"--namespace=team-a", "svc/api", ":8080"}
	if err := saveAlias("api", args, "http"); err != nil {
		t.Fatalf("saveAlias() error = %v", err)
	}

	aliases, err := loadAliases()
	if err != nil {
		t.Fatalf("loadAliases() error = %v", err)
	}
	entry, ok := aliases["api"]
	if !ok {
		t.Fatalf("alias %q not found", "api")
	}
	if !reflect.DeepEqual(entry.Args, args) {
		t.Fatalf("entry.Args = %v, want %v", entry.Args, args)
	}
	if got, want := entry.PortName, "http"; got != want {
		t.Fatalf("entry.PortName = %q, want %q", got, want)
	}
	if entry.UpdatedAtUnix <= 0 {
		t.Fatalf("entry.UpdatedAtUnix = %d, want > 0", entry.UpdatedAtUnix)
	}
}

func TestSaveAliasPreservesExistingPortNameWhenUnknown(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	args := []string{"--namespace=team-a", "svc/api", ":8080"}
	if err := saveAlias("api", args, "http"); err != nil {
		t.Fatalf("saveAlias() first error = %v", err)
	}
	if err := saveAlias("api", args, ""); err != nil {
		t.Fatalf("saveAlias() second error = %v", err)
	}

	aliases, err := loadAliases()
	if err != nil {
		t.Fatalf("loadAliases() error = %v", err)
	}
	if got, want := aliases["api"].PortName, "http"; got != want {
		t.Fatalf("entry.PortName = %q, want %q", got, want)
	}
}

func TestTrimHistory(t *testing.T) {
	t.Parallel()

	history := make(map[string]int64, maxHistoryEntryCount+8)
	for i := 0; i < maxHistoryEntryCount+8; i++ {
		historyKey := fmt.Sprintf("key-%d", i)
		history[historyKey] = int64(i + 1)
	}
	trimHistory(history)
	if got, wantMax := len(history), maxHistoryEntryCount; got > wantMax {
		t.Fatalf("trimHistory() left %d entries, want <= %d", got, wantMax)
	}
}
