#!/usr/bin/env bash
set -euo pipefail

ls -lh 115.index.zip
ls -lh /opt/alist-tvbox/index115/

sudo rm -rf /opt/alist-tvbox/index115/
sudo unzip 115.index.zip -d /opt/alist-tvbox/index115/

ls -lh /opt/alist-tvbox/index115/
