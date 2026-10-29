package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bjarneo/shoulder/internal/config"
)

func TestEditorSkipsOmarchyWrapper(t *testing.T) {
	home, bin := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "omarchy-launch-editor --inline")
	if got := editor(config.Config{}); got != "nvim" {
		t.Fatalf("without an Omarchy default: got %q, want nvim", got)
	}

	if err := os.WriteFile(filepath.Join(bin, "hx"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(home, ".local", "state", "omarchy", "defaults")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "editor"), []byte("hx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := editor(config.Config{}); got != "hx" {
		t.Fatalf("with hx as the Omarchy default: got %q", got)
	}
	if err := os.WriteFile(filepath.Join(state, "editor"), []byte("zed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := editor(config.Config{}); got != "nvim" {
		t.Fatalf("with an Omarchy default that is not installed: got %q, want nvim", got)
	}

	t.Setenv("EDITOR", "vim")
	if got := editor(config.Config{}); got != "vim" {
		t.Fatalf("plain EDITOR: got %q", got)
	}
	if got := editor(config.Config{Editor: "nano"}); got != "nano" {
		t.Fatalf("config editor: got %q", got)
	}
}
