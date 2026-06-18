package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallScriptUsesCmdEntrypointAndEnvFile(t *testing.T) {
	root := filepath.Join("..", "..")
	body, err := os.ReadFile(filepath.Join(root, "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "go build -o ${BIN_NAME} ./cmd/115-indexer/") {
		t.Fatal("install.sh should build from ./cmd/115-indexer/")
	}
	if !strings.Contains(text, "EnvironmentFile=-/opt/${SERVICE_NAME}/.env") {
		t.Fatal("install.sh should load optional .env via systemd EnvironmentFile")
	}
	if !strings.Contains(text, "-mode daemon") || !strings.Contains(text, "-db ${DB_PATH}") || !strings.Contains(text, "-bleve ${BLEVE_PATH}") || !strings.Contains(text, "-admin-addr ${ADMIN_ADDR}") {
		t.Fatal("install.sh should start daemon with db, bleve, and admin addr variables")
	}
	if !strings.Contains(text, "Restart=on-failure") {
		t.Fatal("install.sh should restart only on failure")
	}
	if !strings.Contains(text, "RestartSec=30") {
		t.Fatal("install.sh should wait 30 seconds before restart")
	}
	if !strings.Contains(text, "StartLimitIntervalSec=600") || !strings.Contains(text, "StartLimitBurst=5") {
		t.Fatal("install.sh should configure systemd start rate limiting")
	}
}

func TestDeployScriptUploadsInstallScriptAndEnvFile(t *testing.T) {
	root := filepath.Join("..", "..")
	body, err := os.ReadFile(filepath.Join(root, "deploy.sh"))
	if err != nil {
		t.Fatalf("read deploy.sh: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, `scp "${BIN_NAME}" install.sh "${HOST}:~"`) {
		t.Fatal("deploy.sh should upload install.sh together with the binary")
	}
	if !strings.Contains(text, "if [ -f .env ]; then") || !strings.Contains(text, `scp .env "${HOST}:~/.env"`) {
		t.Fatal("deploy.sh should upload .env when present")
	}
}
