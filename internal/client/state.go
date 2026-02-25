package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	clientStateDirName        = "kpf"
	historyFileName           = "history.json"
	aliasFileName             = "aliases.json"
	maxHistoryEntryCount      = 512
	atomicWriteTempFileSuffix = ".tmp"
)

type historyDB struct {
	// LastUsedByKey is kept for backward compatibility with older kpf versions.
	LastUsedByKey map[string]int64        `json:"last_used_by_key,omitempty"`
	EntriesByKey  map[string]historyEntry `json:"entries_by_key,omitempty"`
}

type historyEntry struct {
	LastUsedUnix int64    `json:"last_used_unix"`
	Args         []string `json:"args,omitempty"`
	PortName     string   `json:"port_name,omitempty"`
}

type aliasDB struct {
	Aliases map[string]aliasEntry `json:"aliases"`
}

type aliasEntry struct {
	Args          []string `json:"args,omitempty"`
	PortName      string   `json:"port_name,omitempty"`
	UpdatedAtUnix int64    `json:"updated_at_unix"`
}

func stateDir() string {
	configDir, err := os.UserConfigDir()
	if err == nil && configDir != "" {
		return filepath.Join(configDir, clientStateDirName)
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("kpf-%d", os.Getuid()))
}

func historyPath() string {
	return filepath.Join(stateDir(), historyFileName)
}

func aliasPath() string {
	return filepath.Join(stateDir(), aliasFileName)
}

func loadHistory() (map[string]int64, error) {
	entries, err := loadHistoryEntries()
	if err != nil {
		return nil, err
	}
	history := make(map[string]int64, len(entries))
	for key, entry := range entries {
		history[key] = entry.LastUsedUnix
	}
	return history, nil
}

func loadHistoryEntries() (map[string]historyEntry, error) {
	var db historyDB
	if err := readJSONFile(historyPath(), &db); err != nil {
		return nil, err
	}

	entries := make(map[string]historyEntry)
	for key, lastUsed := range db.LastUsedByKey {
		entries[key] = historyEntry{LastUsedUnix: lastUsed}
	}
	for key, entry := range db.EntriesByKey {
		merged := entries[key]
		if entry.LastUsedUnix != 0 {
			merged.LastUsedUnix = entry.LastUsedUnix
		}
		if len(entry.Args) > 0 {
			merged.Args = append([]string(nil), entry.Args...)
		}
		if entry.PortName != "" {
			merged.PortName = entry.PortName
		}
		entries[key] = merged
	}

	return entries, nil
}

func recordHistory(args []string, portName string) error {
	key := historyKey(args)
	if key == "" {
		return nil
	}
	entries, err := loadHistoryEntries()
	if err != nil {
		return err
	}
	if existing, ok := entries[key]; ok && portName == "" {
		portName = existing.PortName
	}
	entries[key] = historyEntry{
		LastUsedUnix: time.Now().Unix(),
		Args:         append([]string(nil), args...),
		PortName:     portName,
	}
	trimHistoryEntries(entries)

	history := make(map[string]int64, len(entries))
	entriesByKey := make(map[string]historyEntry, len(entries))
	for key, entry := range entries {
		history[key] = entry.LastUsedUnix
		entriesByKey[key] = historyEntry{
			LastUsedUnix: entry.LastUsedUnix,
			Args:         append([]string(nil), entry.Args...),
			PortName:     entry.PortName,
		}
	}
	return writeJSONFile(historyPath(), historyDB{
		LastUsedByKey: history,
		EntriesByKey:  entriesByKey,
	})
}

func loadAliases() (map[string]aliasEntry, error) {
	var db aliasDB
	if err := readJSONFile(aliasPath(), &db); err != nil {
		return nil, err
	}
	if db.Aliases == nil {
		return map[string]aliasEntry{}, nil
	}

	aliases := make(map[string]aliasEntry, len(db.Aliases))
	for name, entry := range db.Aliases {
		aliases[name] = aliasEntry{
			Args:          append([]string(nil), entry.Args...),
			PortName:      entry.PortName,
			UpdatedAtUnix: entry.UpdatedAtUnix,
		}
	}
	return aliases, nil
}

func saveAlias(name string, args []string, portName string) error {
	if name == "" {
		return errors.New("alias name cannot be empty")
	}
	aliases, err := loadAliases()
	if err != nil {
		return err
	}
	if existing, ok := aliases[name]; ok && portName == "" {
		portName = existing.PortName
	}
	aliases[name] = aliasEntry{
		Args:          append([]string(nil), args...),
		PortName:      portName,
		UpdatedAtUnix: time.Now().Unix(),
	}
	return writeJSONFile(aliasPath(), aliasDB{Aliases: aliases})
}

func trimHistoryEntries(entries map[string]historyEntry) {
	history := make(map[string]int64, len(entries))
	for key, entry := range entries {
		history[key] = entry.LastUsedUnix
	}
	trimHistory(history)
	for key := range entries {
		if _, ok := history[key]; !ok {
			delete(entries, key)
		}
	}
}

func trimHistory(history map[string]int64) {
	if len(history) <= maxHistoryEntryCount {
		return
	}

	type item struct {
		key      string
		lastUsed int64
	}
	items := make([]item, 0, len(history))
	for key, lastUsed := range history {
		items = append(items, item{key: key, lastUsed: lastUsed})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].lastUsed == items[j].lastUsed {
			return items[i].key < items[j].key
		}
		return items[i].lastUsed > items[j].lastUsed
	})
	for _, stale := range items[maxHistoryEntryCount:] {
		delete(history, stale.key)
	}
}

func readJSONFile(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func writeJSONFile(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	data = append(data, '\n')

	tmpPath := path + atomicWriteTempFileSuffix
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
