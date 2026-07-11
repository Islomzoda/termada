package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLogsArgsAcceptsFollowBeforeOrAfterJob(t *testing.T) {
	for _, args := range [][]string{{"-f", "job_1"}, {"job_1", "-f"}, {"job_1", "--follow"}} {
		job, follow, err := parseLogsArgs(args)
		if err != nil || job != "job_1" || !follow {
			t.Fatalf("parseLogsArgs(%v) = %q, %v, %v", args, job, follow, err)
		}
	}
}

func TestParseLogsArgsRejectsInvalidShape(t *testing.T) {
	for _, args := range [][]string{nil, {"one", "two"}, {"--unknown", "job_1"}} {
		if _, _, err := parseLogsArgs(args); err == nil {
			t.Fatalf("parseLogsArgs(%v) should fail", args)
		}
	}
}

func TestVersionMetadataStaysInSync(t *testing.T) {
	root := filepath.Join("..", "..")
	readJSON := func(path string, out any) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
	}

	var server struct {
		Version  string `json:"version"`
		Packages []struct {
			Identifier       string `json:"identifier"`
			RuntimeArguments []any  `json:"runtimeArguments"`
			PackageArguments []struct {
				Type  string `json:"type"`
				Value string `json:"value"`
			} `json:"packageArguments"`
		} `json:"packages"`
	}
	readJSON("server.json", &server)
	if len(server.Packages) != 1 {
		t.Fatalf("server metadata has %d packages, want 1", len(server.Packages))
	}
	var plugin struct {
		Version string `json:"version"`
	}
	readJSON(filepath.Join(".claude-plugin", "plugin.json"), &plugin)

	if server.Version != version || plugin.Version != version {
		t.Fatalf("version drift: binary=%s server=%s plugin=%s", version, server.Version, plugin.Version)
	}
	for _, pkg := range server.Packages {
		if !strings.HasSuffix(pkg.Identifier, ":"+version) {
			t.Fatalf("server package %q does not use version %s", pkg.Identifier, version)
		}
		if len(pkg.RuntimeArguments) != 0 || len(pkg.PackageArguments) != 2 ||
			pkg.PackageArguments[0].Type != "positional" || pkg.PackageArguments[0].Value != "serve" ||
			pkg.PackageArguments[1].Type != "positional" || pkg.PackageArguments[1].Value != "--stdio" {
			t.Fatalf("server package command metadata is invalid: %+v", pkg)
		}
	}
}

func TestSignChecksumsRequiresMatchingKeyPair(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privText := base64.StdEncoding.EncodeToString(priv)
	pubText := base64.StdEncoding.EncodeToString(pub)
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		priv string
		pub  string
	}{
		{name: "no keys"},
		{name: "private key only", priv: privText},
		{name: "public key only", pub: pubText},
		{name: "mismatched keys", priv: base64.StdEncoding.EncodeToString(otherPriv), pub: pubText},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "checksums.txt")
			if err := os.WriteFile(path, []byte("checksums"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := signChecksumsFile(path, tt.priv, tt.pub); err == nil {
				t.Fatal("invalid signing configuration was accepted")
			}
			if _, err := os.Stat(path + ".sig"); !os.IsNotExist(err) {
				t.Fatalf("failed signing created a signature file: %v", err)
			}
		})
	}

	path := filepath.Join(t.TempDir(), "checksums.txt")
	if err := os.WriteFile(path, []byte("checksums"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := signChecksumsFile(path, privText, pubText); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path + ".sig"); err != nil || len(data) == 0 {
		t.Fatalf("signature = %q, err=%v", data, err)
	}
}

func TestDashboardURLUsesRuntimeCompatibleAddress(t *testing.T) {
	got, err := withDashboardToken("http://127.0.0.1:9876/", "a token&value")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://127.0.0.1:9876/?token=a+token%26value" {
		t.Fatalf("tokenized URL = %q", got)
	}
}
