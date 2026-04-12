package main

import (
	"embed"
	"os"
	"path/filepath"
)

//go:embed runtime/*
var embeddedRuntime embed.FS

// writeEmbeddedRuntimeFiles extracts the embedded Sky.Live runtime
// files to the given output directory. Called by the compiler when
// building Sky.Live projects.
func writeEmbeddedRuntimeFiles(outDir string) {
	entries, err := embeddedRuntime.ReadDir("runtime")
	if err != nil {
		return
	}
	for _, entry := range entries {
		// Skip templates/ — only needed by the compiler for sky init,
		// not by user project builds.
		if entry.Name() == "templates" {
			continue
		}
		if entry.IsDir() {
			writeEmbeddedDir(outDir, "runtime/"+entry.Name(), entry.Name())
		} else {
			writeEmbeddedFile(outDir, "runtime/"+entry.Name(), entry.Name())
		}
	}
}

func writeEmbeddedDir(outDir, embedPath, relPath string) {
	dirPath := filepath.Join(outDir, relPath)
	os.MkdirAll(dirPath, 0755)
	entries, err := embeddedRuntime.ReadDir(embedPath)
	if err != nil {
		return
	}
	for _, entry := range entries {
		childEmbed := embedPath + "/" + entry.Name()
		childRel := relPath + "/" + entry.Name()
		if entry.IsDir() {
			writeEmbeddedDir(outDir, childEmbed, childRel)
		} else {
			writeEmbeddedFile(outDir, childEmbed, childRel)
		}
	}
}

func writeEmbeddedFile(outDir, embedPath, relPath string) {
	data, err := embeddedRuntime.ReadFile(embedPath)
	if err != nil {
		return
	}
	dst := filepath.Join(outDir, relPath)
	os.MkdirAll(filepath.Dir(dst), 0755)
	os.WriteFile(dst, data, 0644)
}

// readEmbeddedTemplate reads a template file from the embedded FS.
// Used by sky init to get CLAUDE.md without needing it on disk.
func readEmbeddedTemplate(name string) string {
	data, err := embeddedRuntime.ReadFile("runtime/templates/" + name)
	if err != nil {
		return ""
	}
	return string(data)
}
