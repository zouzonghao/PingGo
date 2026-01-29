#!/bin/sh

# ==============================================================================
# PingGo 服务安装脚本 for POSIX sh
# 兼容 Debian/Ubuntu/Alpine Linux
# ==============================================================================

# -- 脚本配置 --
SERVICE_NAME="pinggo"
INSTALL_DIR="/opt/pinggo"
EXECUTABLE_NAME="pinggo"
GITHUB_REPO="zouzonghao/PingGo"
DEFAULT_PORT="37374"

# -- 颜色定义 (自动检测终端) --
if [ -t 1 ]; then
    GREEN='\033[0;32m'
    RED='\033[0;31m'
    YELLOW='\033[0;33m'
    NC='\033[0m'
else
    GREEN=''
    RED=''
    YELLOW=''
    NC=''
fi

# -- 函数定义 --

die() {
    printf "${RED}错误: %s${NC}\n" "$1" >&2
    exit 1
}

info() {
    printf "${GREEN}信息: %s${NC}\n" "$1"
}

warn() {
    printf "${YELLOW}警告: %s${NC}\n" "$1"
}

# -- 检测操作系统和初始化系统 --
detect_os() {
    if [ -f /etc/os-release ]; then
        # shellcheck source=/dev/null
        . /etc/os-release
        OS_ID="$ID"
        OS_NAME="$PRETTY_NAME"
    else
        die "无法检测操作系统类型。"
    fi

    # 检测初始化系统
    if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
        INIT_SYSTEM="systemd"
    elif command -v rc-service >/dev/null 2>&1; then
        INIT_SYSTEM="openrc"
    else
        die "不支持的初始化系统。仅支持 systemd 和 OpenRC。"
    fi

    info "检测到操作系统: $OS_NAME"
    info "检测到初始化系统: $INIT_SYSTEM"
}

# -- Systemd 服务管理 --
create_systemd_service() {
    SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
    
    info "创建 systemd 服务文件..."
    cat << EOF > "$SERVICE_FILE"
[Unit]
Description=PingGo Monitoring Service
After=network.target

[Service]
Type=simple
User=root
Group=root
WorkingDirectory=${INSTALL_DIR}
ExecStart=${INSTALL_DIR}/${EXECUTABLE_NAME}
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable "${SERVICE_NAME}"
    systemctl start "${SERVICE_NAME}"
}

remove_systemd_service() {
    SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
    
    systemctl stop "${SERVICE_NAME}" >/dev/null 2>&1 || true
    systemctl disable "${SERVICE_NAME}" >/dev/null 2>&1 || true
    rm -f "$SERVICE_FILE"
    systemctl daemon-reload
}

is_systemd_installed() {
    [ -f "/etc/systemd/system/${SERVICE_NAME}.service" ]
}

# -- OpenRC 服务管理 (Alpine) --
create_openrc_service() {
    SERVICE_FILE="/etc/init.d/${SERVICE_NAME}"
    
    info "创建 OpenRC 服务文件..."
    cat << EOF > "$SERVICE_FILE"
#!/sbin/openrc-run

name="pinggo"
description="PingGo Monitoring Service"
command="${INSTALL_DIR}/${EXECUTABLE_NAME}"
command_background="yes"
pidfile="/run/\${RC_SVCNAME}.pid"
directory="${INSTALL_DIR}"

depend() {
    need net
    after firewall
}
EOF

    chmod +x "$SERVICE_FILE"
    rc-update add "${SERVICE_NAME}" default
    rc-service "${SERVICE_NAME}" start
}

remove_openrc_service() {
    SERVICE_FILE="/etc/init.d/${SERVICE_NAME}"
    
    rc-service "${SERVICE_NAME}" stop >/dev/null 2>&1 || true
    rc-update del "${SERVICE_NAME}" default >/dev/null 2>&1 || true
    rm -f "$SERVICE_FILE"
}

is_openrc_installed() {
    [ -f "/etc/init.d/${SERVICE_NAME}" ]
}

# -- 通用服务管理函数 --
create_service() {
    if [ "$INIT_SYSTEM" = "systemd" ]; then
        create_systemd_service
    else
        create_openrc_service
    fi
}

remove_service() {
    if [ "$INIT_SYSTEM" = "systemd" ]; then
        remove_systemd_service
    else
        remove_openrc_service
    fi
}

is_service_installed() {
    if [ "$INIT_SYSTEM" = "systemd" ]; then
        is_systemd_installed
    else
        is_openrc_installed
    fi
}

stop_service() {
    if [ "$INIT_SYSTEM" = "systemd" ]; then
        systemctl stop "${SERVICE_NAME}" >/dev/null 2>&1 || true
    else
        rc-service "${SERVICE_NAME}" stop >/dev/null 2>&1 || true
    fi
}

# -- 检查依赖 --
check_dependencies() {
    command -v curl >/dev/null 2>&1 || die "需要 curl 命令，请先安装。"
    command -v tar >/dev/null 2>&1 || die "需要 tar 命令，请先安装。"
}

# -- 安装函数 --
install_service() {
    info "开始安装 ${SERVICE_NAME} 服务..."

    check_dependencies

    # -- 获取最新版本信息 --
    info "正在获取最新版本信息..."
    LATEST_VERSION=$(curl -s "https://api.github.com/repos/${GITHUB_REPO}/releases" | grep '"tag_name":' | grep '"v[0-9]' | head -n 1 | cut -d '"' -f 4)
    
    if [ -z "$LATEST_VERSION" ]; then
        die "无法获取最新版本号。请检查网络连接。"
    fi
    info "找到最新版本: ${LATEST_VERSION}"

    API_RESPONSE=$(curl -s "https://api.github.com/repos/${GITHUB_REPO}/releases/tags/${LATEST_VERSION}")
    DOWNLOAD_URL=$(echo "$API_RESPONSE" | grep "browser_download_url" | grep "pinggo-linux-amd64.tar.gz" | cut -d '"' -f 4)

    if [ -z "$DOWNLOAD_URL" ]; then
        die "无法获取下载链接。"
    fi

    # -- 检查是否已安装 --
    if is_service_installed; then
        warn "${SERVICE_NAME} 服务已安装。"
        printf "是否要覆盖安装? (y/n): "
        read -r REPLY
        case "$REPLY" in
            [Yy]*) 
                info "正在停止服务以进行覆盖安装..."
                stop_service
                ;;
            *) 
                info "安装已取消。"
                exit 0 
                ;;
        esac
    fi

    # -- 数据库处理 --
    DATABASE_FILE="${INSTALL_DIR}/pinggo.db"
    if [ -f "$DATABASE_FILE" ]; then
        printf "是否要重置数据库? (警告: 这将删除所有现有数据) (y/n): "
        read -r REPLY
        case "$REPLY" in
            [Yy]*) 
                info "正在重置数据库..."
                rm -f "$DATABASE_FILE" || die "删除数据库失败。"
                ;;
            *) info "保留现有数据库。" ;;
        esac
    fi

    # -- 下载并安装 --
    info "创建安装目录: ${INSTALL_DIR}"
    mkdir -p "$INSTALL_DIR" || die "无法创建安装目录。"

    TEMP_FILE="/tmp/pinggo-download.tar.gz"
    info "从 GitHub 下载文件..."
    curl -L -o "$TEMP_FILE" "$DOWNLOAD_URL" || die "下载文件失败。"

    info "解压文件到 ${INSTALL_DIR}..."
    # 保留数据库和配置文件
    find "$INSTALL_DIR" -mindepth 1 ! -name 'pinggo.db' ! -name 'config.yaml' -exec rm -rf {} + 2>/dev/null || true
    tar -xzf "$TEMP_FILE" -C "$INSTALL_DIR" || die "解压文件失败。"

    # 重命名可执行文件
    ORIGINAL_EXECUTABLE="${INSTALL_DIR}/pinggo-linux-amd64"
    TARGET_EXECUTABLE="${INSTALL_DIR}/${EXECUTABLE_NAME}"
    
    if [ -f "$ORIGINAL_EXECUTABLE" ]; then
        mv "$ORIGINAL_EXECUTABLE" "$TARGET_EXECUTABLE" || die "重命名文件失败。"
    fi
    chmod +x "$TARGET_EXECUTABLE" || die "设置执行权限失败。"

    # -- 创建服务 --
    create_service
    
    # -- 记录版本号 --
    echo "$LATEST_VERSION" > "${INSTALL_DIR}/.version" || warn "无法写入版本文件。"
    
    rm -f "$TEMP_FILE"

    info "----------------------------------------------------"
    printf "${GREEN}安装成功! (${LATEST_VERSION})${NC}\n"
    if [ "$INIT_SYSTEM" = "systemd" ]; then
        printf "服务状态检查: ${YELLOW}sudo systemctl status ${SERVICE_NAME}${NC}\n"
        printf "查看服务日志: ${YELLOW}sudo journalctl -u ${SERVICE_NAME} -f${NC}\n"
    else
        printf "服务状态检查: ${YELLOW}sudo rc-service ${SERVICE_NAME} status${NC}\n"
        printf "查看服务日志: ${YELLOW}cat /var/log/${SERVICE_NAME}.log${NC}\n"
    fi
    printf "程序监听端口: ${YELLOW}${DEFAULT_PORT}${NC}\n"
    printf "Web 访问地址: ${YELLOW}http://localhost:${DEFAULT_PORT}${NC}\n"
    info "----------------------------------------------------"
}

# -- 卸载函数 --
uninstall_service() {
    info "开始卸载 ${SERVICE_NAME} 服务..."
    
    if ! is_service_installed; then
        warn "${SERVICE_NAME} 服务未安装。"
        return
    fi

    # -- 数据库处理 --
    DATABASE_FILE="${INSTALL_DIR}/pinggo.db"
    DELETE_DB=1
    if [ -f "$DATABASE_FILE" ]; then
        printf "是否要删除数据库文件? (警告: 数据将无法恢复) (y/n): "
        read -r REPLY
        case "$REPLY" in
            [Yy]*) info "数据库将被删除。" ;;
            *) info "将保留数据库文件: ${DATABASE_FILE}"; DELETE_DB=0 ;;
        esac
    fi

    # -- 停止并移除服务 --
    remove_service

    # -- 删除安装目录 --
    info "删除安装目录..."
    if [ "$DELETE_DB" -eq 1 ]; then
        rm -rf "$INSTALL_DIR"
    else
        rm -f "${INSTALL_DIR}/${EXECUTABLE_NAME}"
        rm -f "${INSTALL_DIR}/.version"
        # 如果目录只剩数据库，保留它
    fi
    
    info "${GREEN}卸载完成！${NC}"
}

# -- 主逻辑 --
main() {
    if [ "$(id -u)" -ne 0 ]; then
        die "此脚本需要以 root 权限运行。请使用 sudo。"
    fi

    detect_os

    case "$1" in
        install)
            install_service
            ;;
        uninstall)
            uninstall_service
            ;;
        *)
            printf "PingGo 安装脚本\n"
            printf "用法: %s {install|uninstall}\n\n" "$0"
            printf "命令:\n"
            printf "  install    安装或更新 PingGo 服务\n"
            printf "  uninstall  卸载 PingGo 服务\n"
            exit 1
            ;;
    esac
}

main "$@"
