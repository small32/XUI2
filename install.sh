#!/bin/bash

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
plain='\033[0m'

cur_dir=$(pwd)

# ==================== 发布渠道（写死，不混用） ====================
# 本文件在本渠道写死本渠道地址：运行期不推断渠道、不回退其他渠道、不读渠道状态文件。
# 另一渠道的同一文件内容不同；改动本块后必须同步另一渠道的同一文件。
XUI_API_URL="https://api.github.com/repos/small32/XUI2/releases/latest"
XUI_RELEASE_URL="https://github.com/small32/XUI2/releases/download"
XUI_RAW_URL="https://raw.githubusercontent.com/small32/XUI2/main"
XUI_RELEASES_PAGE="https://github.com/small32/XUI2/releases"

# 从 releases/latest 的 JSON 中取出 tag_name
parse_tag_name() {
    grep -Eo '"tag_name": *"[^"]+"' | head -1 | sed 's/.*: *"//; s/"$//'
}
# ======================================================

# check root
# EUID=0 即当前已具备 root 权限（真 root 或以 sudo 提权），否则拒绝执行。
[[ $EUID -ne 0 ]] && echo -e "${red}错误：${plain} 请在 root 用户或 sudo 权限下执行此脚本！\n" >&2 && exit 1

# check os
if [[ -f /etc/redhat-release ]]; then
    release="centos"
elif cat /etc/issue | grep -Eqi "debian"; then
    release="debian"
elif cat /etc/issue | grep -Eqi "ubuntu"; then
    release="ubuntu"
elif cat /etc/issue | grep -Eqi "centos|red hat|redhat"; then
    release="centos"
elif cat /proc/version | grep -Eqi "debian"; then
    release="debian"
elif cat /proc/version | grep -Eqi "ubuntu"; then
    release="ubuntu"
elif cat /proc/version | grep -Eqi "centos|red hat|redhat"; then
    release="centos"
else
    echo -e "${red}未检测到系统版本，请联系脚本作者！${plain}\n" && exit 1
fi

arch=$(arch)

if [[ $arch == "x86_64" || $arch == "x64" || $arch == "amd64" ]]; then
    arch="amd64"
elif [[ $arch == "aarch64" || $arch == "arm64" ]]; then
    arch="arm64"
else
    echo -e "${red}不支持的 CPU 架构: ${arch}。本程序仅提供 amd64 与 arm64 安装包，不支持 s390x 等其他架构。${plain}"
    exit 1
fi

echo "架构: ${arch}"

if [ $(getconf WORD_BIT) != '32' ] && [ $(getconf LONG_BIT) != '64' ]; then
    echo "本软件不支持 32 位系统(x86)，请使用 64 位系统(x86_64)，如果检测有误，请联系作者"
    exit -1
fi

os_version=""

# os version
if [[ -f /etc/os-release ]]; then
    os_version=$(awk -F'[= ."]' '/VERSION_ID/{print $3}' /etc/os-release)
fi
if [[ -z "$os_version" && -f /etc/lsb-release ]]; then
    os_version=$(awk -F'[= ."]+' '/DISTRIB_RELEASE/{print $2}' /etc/lsb-release)
fi

if [[ x"${release}" == x"centos" ]]; then
    if [[ ${os_version} -le 6 ]]; then
        echo -e "${red}请使用 CentOS 7 或更高版本的系统！${plain}\n" && exit 1
    fi
elif [[ x"${release}" == x"ubuntu" ]]; then
    if [[ ${os_version} -lt 16 ]]; then
        echo -e "${red}请使用 Ubuntu 16 或更高版本的系统！${plain}\n" && exit 1
    fi
elif [[ x"${release}" == x"debian" ]]; then
    if [[ ${os_version} -lt 8 ]]; then
        echo -e "${red}请使用 Debian 8 或更高版本的系统！${plain}\n" && exit 1
    fi
fi

install_base() {
    if [[ x"${release}" == x"centos" ]]; then
        yum install wget curl tar unzip git gcc python3 openssl -y
    else
        apt install wget curl tar unzip git gcc python3 openssl -y
    fi
}

choose_role() {
    local previous=""
    if [[ -f /etc/xui/role.env ]]; then
        previous=$(sed -n 's/^XUI_ROLE=//p' /etc/xui/role.env | head -1)
    fi
    if [[ -z "$XUI_ROLE" ]]; then
        echo "请选择安装角色：1) 服务端（管理端）  2) 被控端"
        if [[ "$previous" == "manager" ]]; then
            read -r -p "角色 [1，保持当前服务端]: " selection
        elif [[ "$previous" == "agent" ]]; then
            read -r -p "角色 [2，保持当前被控端]: " selection
        else
            read -r -p "角色 [1/2]: " selection
        fi
        case "$selection" in
            1) XUI_ROLE=manager ;;
            2) XUI_ROLE=agent ;;
            "") XUI_ROLE="$previous" ;;
            *) echo "角色选择无效"; exit 1 ;;
        esac
    fi
    [[ "$XUI_ROLE" == manager || "$XUI_ROLE" == agent ]] || { echo "必须选择管理端或被控端"; exit 1; }
    if [[ -d /etc/xui && -f /etc/xui/xui.db && -n "$previous" && "$previous" != "$XUI_ROLE" ]]; then
        echo "已有 $previous 数据库，不能原地切换为 $XUI_ROLE。请在新机器或新数据目录部署。"
        exit 1
    fi
}

build_source_archive() {
    local out="$1" build_dir go_version xray_version xray_asset
    build_dir=$(mktemp -d /tmp/xui2-build.XXXXXX) || return 1
    echo "当前仓库尚无 Release，从 XUI2/main 构建安装包。"
    git clone --depth 1 https://github.com/small32/XUI2.git "$build_dir/source" || return 1
    go_version=$(curl -fsSL https://go.dev/dl/?mode=json | python3 -c 'import json,sys; print(next(v["version"] for v in json.load(sys.stdin) if v["stable"]))') || return 1
    curl -fL --retry 3 "https://go.dev/dl/${go_version}.linux-${arch}.tar.gz" -o "$build_dir/go.tar.gz" || return 1
    mkdir -p "$build_dir/toolchain" "$build_dir/package/xui/bin"
    tar -xzf "$build_dir/go.tar.gz" -C "$build_dir/toolchain" || return 1
    (cd "$build_dir/source" && PATH="$build_dir/toolchain/go/bin:$PATH" CGO_ENABLED=1 "$build_dir/toolchain/go/bin/go" build -trimpath -o "$build_dir/package/xui/xui" main.go) || return 1
    cp "$build_dir/source/xui.service" "$build_dir/source/xui.sh" "$build_dir/source/LICENSE" "$build_dir/package/xui/"
    xray_version="v26.3.27"
    if [[ "$arch" == amd64 ]]; then xray_asset="Xray-linux-64.zip"; else xray_asset="Xray-linux-arm64-v8a.zip"; fi
    curl -fL --retry 3 "https://github.com/XTLS/Xray-core/releases/download/${xray_version}/${xray_asset}" -o "$build_dir/xray.zip" || return 1
    unzip -jo "$build_dir/xray.zip" xray -d "$build_dir/package/xui/bin" || return 1
    mv "$build_dir/package/xui/bin/xray" "$build_dir/package/xui/bin/xray-linux-${arch}"
    for data_file in geoip.dat geosite.dat; do
        curl -fL --retry 3 "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/${data_file}" -o "$build_dir/package/xui/bin/${data_file}" || return 1
    done
    tar -C "$build_dir/package" -czf "$out" xui || return 1
    rm -rf "$build_dir"
}

configure_role() {
    mkdir -p /etc/xui
    chmod 700 /etc/xui
    printf 'XUI_ROLE=%s\n' "$XUI_ROLE" > /etc/xui/role.env
    chmod 600 /etc/xui/role.env
    if [[ "$XUI_ROLE" == agent && ! -f /etc/xui/agent.env ]]; then
        printf 'XUI_AGENT_TOKEN=%s\n' "$(openssl rand -hex 32)" > /etc/xui/agent.env
        chmod 600 /etc/xui/agent.env
    fi
    if [[ ! -f /etc/xui/panel.crt || ! -f /etc/xui/panel.key ]]; then
        read -r -p "本机面板的主机名或 IP [$(hostname -f)]: " api_host
        api_host="${api_host:-$(hostname -f)}"
        if [[ "$api_host" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ || "$api_host" == *:* ]]; then
            alt_name="IP:${api_host}"
        else
            alt_name="DNS:${api_host}"
        fi
        openssl req -x509 -newkey rsa:3072 -nodes -days 365 -keyout /etc/xui/panel.key -out /etc/xui/panel.crt -subj "/CN=${api_host}" -addext "subjectAltName=${alt_name}" || return 1
        chmod 600 /etc/xui/panel.key
        chmod 644 /etc/xui/panel.crt
    fi
    /usr/local/xui/xui setting -cert /etc/xui/panel.crt -key /etc/xui/panel.key || return 1
    if [[ "$XUI_ROLE" != agent ]]; then return 0; fi
    echo "被控端 API 令牌（请复制到管理端，仅管理员可见）："
    sed -n 's/^XUI_AGENT_TOKEN=//p' /etc/xui/agent.env
    echo "证书 SHA256 指纹（填入管理端）："
    openssl x509 -in /etc/xui/panel.crt -outform DER | sha256sum | awk '{print $1}'
}

#This function will be called when user installed xui out of sercurity
config_after_install() {
    echo -e "${yellow}出于安全考虑，安装/更新完成后需要强制修改端口与账户密码${plain}"
    read -p "确认是否继续?[y/n]": config_confirm
    if [[ x"${config_confirm}" == x"y" || x"${config_confirm}" == x"Y" ]]; then
        read -p "请设置您的账户名:" config_account
        echo -e "${yellow}您的账户名将设定为:${config_account}${plain}"
        read -r -p "请设置您的账户密码:" config_password
        read -p "请设置面板访问端口:" config_port
        echo -e "${yellow}您的面板访问端口将设定为:${config_port}${plain}"
        echo -e "${yellow}确认设定,设定中${plain}"
        /usr/local/xui/xui setting -username "${config_account}" -password "${config_password}"
        echo -e "${yellow}账户密码设定完成${plain}"
        /usr/local/xui/xui setting -port "${config_port}"
        echo -e "${yellow}面板端口设定完成${plain}"
    else
        echo -e "${red}已取消,所有设置项均为默认设置,请及时修改${plain}"
    fi
}

install_xui() {
    cd /usr/local/

    if [ $# == 0 ]; then
        last_version=$(curl -fsSL --connect-timeout 6 --max-time 20 "$XUI_API_URL" 2>/dev/null | parse_tag_name)
        if [[ -n "$last_version" ]]; then echo "检测到 XUI2 最新版本：$last_version"; fi
    else
        last_version=$1
        echo -e "开始安装 xui v$1"
    fi

    pkg_path="/usr/local/xui-linux-${arch}.tar.gz"
    if [[ -n "$last_version" ]]; then
        pkg_url="${XUI_RELEASE_URL}/${last_version}/xui-linux-${arch}.tar.gz"
        if ! curl -fL --retry 3 -o "$pkg_path" "$pkg_url"; then
            echo "下载发行包失败"; exit 1
        fi
    else
        build_source_archive "$pkg_path" || { echo "从源码构建安装包失败"; exit 1; }
        last_version="source"
    fi

    staging_dir=$(mktemp -d /usr/local/xui-staging.XXXXXX) || exit 1
    if ! tar -xzf xui-linux-${arch}.tar.gz -C "$staging_dir"; then
        echo -e "${red}安装包解压失败，保留当前安装${plain}"; rm -rf "$staging_dir"; exit 1
    fi
    if [[ ! -x "$staging_dir/xui/xui" || ! -x "$staging_dir/xui/bin/xray-linux-${arch}" ]]; then
        echo -e "${red}安装包内容不完整，保留当前安装${plain}"; rm -rf "$staging_dir"; exit 1
    fi
    rm -f xui-linux-${arch}.tar.gz
    systemctl stop xui
    old_dir="/usr/local/xui"
    backup_dir="/usr/local/xui.previous"
    rm -rf "$backup_dir"
    if [[ -e "$old_dir" ]]; then mv "$old_dir" "$backup_dir"; fi
    mv "$staging_dir/xui" "$old_dir" || { [[ -e "$backup_dir" ]] && mv "$backup_dir" "$old_dir"; rm -rf "$staging_dir"; exit 1; }
    rm -rf "$staging_dir"
    cd "$old_dir"
    chmod +x xui bin/xray-linux-${arch}
    cp -f xui.service /etc/systemd/system/
    cp -f xui.sh /usr/bin/xui
    chmod +x /usr/local/xui/xui.sh
    chmod +x /usr/bin/xui
    configure_role || { echo "角色配置失败"; exit 1; }
    config_after_install
    #echo -e "如果是全新安装，默认网页端口为 ${green}54321${plain}，用户名和密码默认都是 ${green}admin${plain}"
    #echo -e "请自行确保此端口没有被其他程序占用，${yellow}并且确保 54321 端口已放行${plain}"
    #    echo -e "若想将 54321 修改为其它端口，输入 xui 命令进行修改，同样也要确保你修改的端口也是放行的"
    #echo -e ""
    #echo -e "如果是更新面板，则按你之前的方式访问面板"
    #echo -e ""
    systemctl daemon-reload
    systemctl enable xui
    start_ok=1
    systemctl start xui || start_ok=0
    # Type=simple reports success as soon as the process is spawned. Wait for
    # the process to remain active so startup/configuration failures still roll
    # back before the previous installation is discarded.
    if [[ $start_ok -eq 1 ]]; then
        start_ok=0
        active_checks=0
        for attempt in {1..5}; do
            if systemctl is-active --quiet xui; then
                active_checks=$((active_checks + 1))
                if [[ $active_checks -eq 5 ]]; then
                    start_ok=1
                    break
                fi
            else
                active_checks=0
            fi
            sleep 1
        done
    fi
    if [[ $start_ok -ne 1 ]]; then
        echo -e "${red}新版本启动失败，正在恢复旧版本${plain}"
        rm -rf "$old_dir"
        [[ -e "$backup_dir" ]] && mv "$backup_dir" "$old_dir"
        systemctl daemon-reload
        systemctl start xui
        exit 1
    fi
    rm -rf "$backup_dir"

    echo -e "${green}xui v${last_version}${plain} 安装完成，面板已启动，"
    echo -e "发行页：${green}${XUI_RELEASES_PAGE}${plain}，后续升级从同一发行页获取。"
    echo -e ""
    echo -e "xui 管理脚本使用方法: "
    echo -e "----------------------------------------------"
    echo -e "xui              - 显示管理菜单 (功能更多)"
    echo -e "xui start        - 启动 xui 面板"
    echo -e "xui stop         - 停止 xui 面板"
    echo -e "xui restart      - 重启 xui 面板"
    echo -e "xui status       - 查看 xui 状态"
    echo -e "xui enable       - 设置 xui 开机自启"
    echo -e "xui disable      - 取消 xui 开机自启"
    echo -e "xui log          - 查看 xui 日志"
    echo -e "xui v2-ui        - 迁移本机器的 v2-ui 账号数据至 xui"
    echo -e "xui update       - 更新 xui 面板"
    echo -e "xui install      - 安装 xui 面板"
    echo -e "xui uninstall    - 卸载 xui 面板"
    echo -e "----------------------------------------------"
}

echo -e "${green}开始安装${plain}"
choose_role
install_base
install_xui $1
