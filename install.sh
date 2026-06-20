#!/usr/bin/env bash
set -euo pipefail

SERVICE_NAME=five
INSTALL_DIR=/opt/${SERVICE_NAME}
BIN_NAME=five
SERVICE_FILE=/etc/systemd/system/${SERVICE_NAME}.service
USER=five
DB_PATH=data/index.db
BLEVE_PATH=data/bleve
ADMIN_ADDR=:8080

echo "==> 停止旧服务（如果存在）"
sudo systemctl stop ${SERVICE_NAME} 2>/dev/null || true

echo "==> 创建运行用户（如果不存在）"
id -u ${USER} &>/dev/null || sudo useradd -r -s /sbin/nologin ${USER}

echo "==> 创建目录"
sudo mkdir -p ${INSTALL_DIR}
sudo mkdir -p ${INSTALL_DIR}/data

[ -f ${BIN_NAME} ] || go build -o ${BIN_NAME} ./cmd/115-indexer/

echo "==> 复制二进制"
sudo cp ./${BIN_NAME} ${INSTALL_DIR}/
sudo chmod +x ${INSTALL_DIR}/${BIN_NAME}

if [ -f .env ]; then
  echo "==> 复制 .env"
  sudo cp ./.env ${INSTALL_DIR}/.env
  sudo chmod 600 ${INSTALL_DIR}/.env
fi

echo "==> 设置权限"
sudo chown -R ${USER}:${USER} ${INSTALL_DIR}

# ===== systemd =====
echo "==> 写入 systemd 服务"

sudo tee ${SERVICE_FILE} > /dev/null <<EOF
[Unit]
Description=115 Share Service
After=network.target
StartLimitIntervalSec=600
StartLimitBurst=5

[Service]
Type=simple
User=${USER}
WorkingDirectory=${INSTALL_DIR}
EnvironmentFile=-/opt/${SERVICE_NAME}/.env
ExecStart=${INSTALL_DIR}/${BIN_NAME} -mode daemon -db ${DB_PATH} -bleve ${BLEVE_PATH} -admin-addr ${ADMIN_ADDR}

Restart=on-failure
RestartSec=30
TimeoutStopSec=30

# 提高文件句柄
LimitNOFILE=1048576

# 安全限制（建议开启）
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true

# 日志
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

echo "==> 重新加载 systemd"
sudo systemctl daemon-reexec
sudo systemctl daemon-reload

echo "==> 启用服务"
sudo systemctl enable ${SERVICE_NAME}

echo "==> 启动服务"
sudo systemctl restart ${SERVICE_NAME}

echo "==> 检查状态"
sleep 1
sudo systemctl status ${SERVICE_NAME} --no-pager || true

echo ""
echo "✅ 部署完成"
echo "👉 服务端口: http://localhost:8080"
echo "👉 查看日志: journalctl -u ${SERVICE_NAME} -f"
