package main

import (
	"path/filepath"
	"testing"
)

func TestHasOpFlag(t *testing.T) {
	ops := [][]string{
		{"-service", "install"},
		{"--service=status"},
		{"-version"},
		{"-help"},
		{"-h"},
		{"-engine-repair", "-data", `C:\Data`, "-repair-msi", `C:\x.msi`},
		{"-addr", ":8080", "-service", "stop"},
	}
	for _, args := range ops {
		if !hasOpFlag(args) {
			t.Errorf("%v must be recognised as an operation invocation", args)
		}
	}
	plain := [][]string{
		nil,
		{},
		{"-addr", ":8080"},
		{"-data", `C:\Data`, "-demo"},
		{"-canonical-child"},
		{"-no-takeover"},
	}
	for _, args := range plain {
		if hasOpFlag(args) {
			t.Errorf("%v must stay an interactive (GUI) start", args)
		}
	}
}

func TestPathUnderAnyRoot(t *testing.T) {
	pf := filepath.Join("C:", "Program Files")
	installed := filepath.Join(pf, "NetGold Monitor", "NetGoldMonitor.exe")
	if !pathUnderAnyRoot(installed, []string{pf}) {
		t.Errorf("installed copy must be recognised as under Program Files: %s", installed)
	}
	downloads := filepath.Join("C:", "Users", "Test", "Downloads", "NetGold-Monitor-v3.0.0.exe")
	if pathUnderAnyRoot(downloads, []string{pf}) {
		t.Errorf("a Downloads copy must not count as installed: %s", downloads)
	}
	// A sibling folder sharing the prefix is not "under" the root.
	sibling := filepath.Join("C:", "Program Files Backup", "x.exe")
	if pathUnderAnyRoot(sibling, []string{pf}) {
		t.Errorf("prefix-sibling folder must not match: %s", sibling)
	}
	if pathUnderAnyRoot(installed, nil) || pathUnderAnyRoot(installed, []string{""}) {
		t.Error("empty roots must never match")
	}
}
