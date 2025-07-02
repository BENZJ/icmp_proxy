#!/bin/bash

# 这个脚本构建 ICMP 客户端。

# 如果任何命令以非零状态退出，则立即退出。
set -e

echo "正在构建客户端..."
# -o 标志将输出的二进制文件放在项目根目录中。
go build -o icmptun_client ./client

echo "构建成功。"
echo "你现在可以运行 ./icmptun_client"
echo "请记得在另一个终端中使用 'sudo ./build_and_run_server.sh' 运行服务端"