#!/bin/bash

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
plain='\033[0m'

#Add some basic function here
function LOGD() {
    echo -e "${yellow}[DEG] $* ${plain}"
}

function LOGE() {
    echo -e "${red}[ERR] $* ${plain}"
}

function LOGI() {
    echo -e "${green}[INF] $* ${plain}"
}

# ==================== 发布渠道（写死，不混用） ====================
# 本文件在本渠道写死本渠道地址：运行期不推断渠道、不回退其他渠道、不读渠道状态文件。
# 另一渠道的同一文件内容不同；改动本块后必须同步另一渠道的同一文件。
XUI_RAW_URL="https://raw.githubusercontent.com/small32/XUI2/main"
# ======================================================
# check root
[[ $EUID -ne 0 ]] && LOGE "错误:  请在 root 用户或 sudo 权限下执行此脚本!\n" && exit 1

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
    LOGE "未检测到系统版本，请联系脚本作者！\n" && exit 1
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
        LOGE "请使用 CentOS 7 或更高版本的系统！\n" && exit 1
    fi
elif [[ x"${release}" == x"ubuntu" ]]; then
    if [[ ${os_version} -lt 16 ]]; then
        LOGE "请使用 Ubuntu 16 或更高版本的系统！\n" && exit 1
    fi
elif [[ x"${release}" == x"debian" ]]; then
    if [[ ${os_version} -lt 8 ]]; then
        LOGE "请使用 Debian 8 或更高版本的系统！\n" && exit 1
    fi
fi

confirm() {
    if [[ $# > 1 ]]; then
        echo && read -p "$1 [默认$2]: " temp
        if [[ x"${temp}" == x"" ]]; then
            temp=$2
        fi
    else
        read -p "$1 [y/n]: " temp
    fi
    if [[ x"${temp}" == x"y" || x"${temp}" == x"Y" ]]; then
        return 0
    else
        return 1
    fi
}

confirm_restart() {
    confirm "是否重启面板，重启面板也会重启 xray" "y"
    if [[ $? == 0 ]]; then
        restart
    else
        show_menu
    fi
}

before_show_menu() {
    echo && echo -n -e "${yellow}按回车返回主菜单: ${plain}" && read temp
    show_menu
}

install() {
    bash <(curl -Ls "${XUI_RAW_URL}/install.sh")
    if [[ $? == 0 ]]; then
        if [[ $# == 0 ]]; then
            start
        else
            start 0
        fi
    fi
}

update() {
    confirm "本功能会强制重装当前最新版，数据不会丢失，是否继续?" "n"
    if [[ $? != 0 ]]; then
        LOGE "已取消"
        if [[ $# == 0 ]]; then
            before_show_menu
        fi
        return 0
    fi
    bash <(curl -Ls "${XUI_RAW_URL}/install.sh")
    if [[ $? == 0 ]]; then
        LOGI "更新完成，已自动重启面板 "
        exit 0
    fi
}

uninstall() {
    confirm "确定要卸载面板吗,xray 也会卸载?" "n"
    if [[ $? != 0 ]]; then
        if [[ $# == 0 ]]; then
            show_menu
        fi
        return 0
    fi
    systemctl stop xui
    systemctl disable xui
    rm /etc/systemd/system/xui.service -f
    systemctl daemon-reload
    systemctl reset-failed
    rm /etc/xui/ -rf
    rm /usr/local/xui/ -rf

    echo ""
    echo -e "卸载成功，如果你想删除此脚本，则退出脚本后运行 ${green}rm /usr/bin/xui -f${plain} 进行删除"
    echo ""

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

reset_user() {
    confirm "确定要将用户名和密码重置为 admin 吗" "n"
    if [[ $? != 0 ]]; then
        if [[ $# == 0 ]]; then
            show_menu
        fi
        return 0
    fi
    /usr/local/xui/xui setting -username admin -password admin
    echo -e "用户名和密码已重置为 ${green}admin${plain}，现在请重启面板"
    confirm_restart
}

reset_config() {
    confirm "确定要重置所有面板设置吗，账号数据不会丢失，用户名和密码不会改变" "n"
    if [[ $? != 0 ]]; then
        if [[ $# == 0 ]]; then
            show_menu
        fi
        return 0
    fi
    /usr/local/xui/xui setting -reset
    echo -e "所有面板设置已重置为默认值，现在请重启面板，并使用默认的 ${green}54321${plain} 端口访问面板"
    confirm_restart
}

check_config() {
    info=$(/usr/local/xui/xui setting -show true)
    if [[ $? != 0 ]]; then
        LOGE "get current settings error,please check logs"
        show_menu
    fi
    LOGI "${info}"
}

set_port() {
    echo && echo -n -e "输入端口号[1-65535]: " && read port
    if [[ -z "${port}" ]]; then
        LOGD "已取消"
        before_show_menu
    else
        /usr/local/xui/xui setting -port ${port}
        echo -e "设置端口完毕，现在请重启面板，并使用新设置的端口 ${green}${port}${plain} 访问面板"
        confirm_restart
    fi
}

start() {
    check_status
    if [[ $? == 0 ]]; then
        echo ""
        LOGI "面板已运行，无需再次启动，如需重启请选择重启"
    else
        systemctl start xui
        sleep 2
        check_status
        if [[ $? == 0 ]]; then
            LOGI "xui 启动成功"
        else
            LOGE "面板启动失败，可能是因为启动时间超过了两秒，请稍后查看日志信息"
        fi
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

stop() {
    check_status
    if [[ $? == 1 ]]; then
        echo ""
        LOGI "面板已停止，无需再次停止"
    else
        systemctl stop xui
        sleep 2
        check_status
        if [[ $? == 1 ]]; then
            LOGI "xui 与 xray 停止成功"
        else
            LOGE "面板停止失败，可能是因为停止时间超过了两秒，请稍后查看日志信息"
        fi
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

restart() {
    systemctl restart xui
    sleep 2
    check_status
    if [[ $? == 0 ]]; then
        LOGI "xui 与 xray 重启成功"
    else
        LOGE "面板重启失败，可能是因为启动时间超过了两秒，请稍后查看日志信息"
    fi
    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

status() {
    systemctl status xui -l
    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

enable() {
    systemctl enable xui
    if [[ $? == 0 ]]; then
        LOGI "xui 设置开机自启成功"
    else
        LOGE "xui 设置开机自启失败"
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

disable() {
    systemctl disable xui
    if [[ $? == 0 ]]; then
        LOGI "xui 取消开机自启成功"
    else
        LOGE "xui 取消开机自启失败"
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

show_log() {
    journalctl -u xui.service -e --no-pager -f
    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

migrate_v2_ui() {
    /usr/local/xui/xui v2-ui

    before_show_menu
}

install_bbr() {
    # temporary workaround for installing bbr
    bash <(curl -L -s https://raw.githubusercontent.com/teddysun/across/master/bbr.sh)
    echo ""
    before_show_menu
}

update_shell() {
    curl -fL "${XUI_RAW_URL}/xui.sh" -o /usr/bin/xui
    if [[ $? != 0 ]]; then
        echo ""
        LOGE "下载脚本失败，请检查本机能否连接 GitHub"
        before_show_menu
    else
        chmod +x /usr/bin/xui
        LOGI "升级脚本成功，请重新运行脚本" && exit 0
    fi
}

# 0: running, 1: not running, 2: not installed
check_status() {
    if [[ ! -f /etc/systemd/system/xui.service ]]; then
        return 2
    fi
    temp=$(systemctl status xui | grep Active | awk '{print $3}' | cut -d "(" -f2 | cut -d ")" -f1)
    if [[ x"${temp}" == x"running" ]]; then
        return 0
    else
        return 1
    fi
}

check_enabled() {
    temp=$(systemctl is-enabled xui)
    if [[ x"${temp}" == x"enabled" ]]; then
        return 0
    else
        return 1
    fi
}

check_uninstall() {
    check_status
    if [[ $? != 2 ]]; then
        echo ""
        LOGE "面板已安装，请不要重复安装"
        if [[ $# == 0 ]]; then
            before_show_menu
        fi
        return 1
    else
        return 0
    fi
}

check_install() {
    check_status
    if [[ $? == 2 ]]; then
        echo ""
        LOGE "请先安装面板"
        if [[ $# == 0 ]]; then
            before_show_menu
        fi
        return 1
    else
        return 0
    fi
}

show_status() {
    check_status
    case $? in
    0)
        echo -e "面板状态: ${green}已运行${plain}"
        show_enable_status
        ;;
    1)
        echo -e "面板状态: ${yellow}未运行${plain}"
        show_enable_status
        ;;
    2)
        echo -e "面板状态: ${red}未安装${plain}"
        ;;
    esac
    # 管理端不运行 xray 进程，状态区不显示 xray 状态；仅被控端显示。
    if [[ "$(sed -n 's/^XUI_ROLE=//p' /etc/xui/role.env 2>/dev/null | head -1)" != "manager" ]]; then
        show_xray_status
    fi
}

show_enable_status() {
    check_enabled
    if [[ $? == 0 ]]; then
        echo -e "是否开机自启: ${green}是${plain}"
    else
        echo -e "是否开机自启: ${red}否${plain}"
    fi
}

check_xray_status() {
    count=$(ps -ef | grep "xray-linux" | grep -v "grep" | wc -l)
    if [[ count -ne 0 ]]; then
        return 0
    else
        return 1
    fi
}

show_xray_status() {
    check_xray_status
    if [[ $? == 0 ]]; then
        echo -e "xray 状态: ${green}运行${plain}"
    else
        echo -e "xray 状态: ${red}未运行${plain}"
    fi
}

ssl_cert_issue() {
    echo -E ""
    LOGD "******使用说明******"
    LOGI "该脚本将使用Acme脚本申请证书,使用时需保证:"
    LOGI "1.知晓Cloudflare 注册邮箱"
    LOGI "2.知晓Cloudflare Global API Key"
    LOGI "3.域名已通过Cloudflare进行解析到当前服务器"
    LOGI "4.该脚本申请的证书默认安装到 /root/cert 目录"
    confirm "我已确认以上内容[y/n]" "y"
    if [ $? -eq 0 ]; then
        local certPath=/root/cert
        local acme_sh=~/.acme.sh/acme.sh
        LOGI "安装Acme脚本"
        if [ ! -x "$acme_sh" ]; then
            curl https://get.acme.sh | sh
            if [ $? -ne 0 ]; then
                LOGE "安装acme脚本失败"
                exit 1
            fi
        fi
        # 不再清空 /root/cert：新证书由 --installcert 直接覆盖旧文件，
        # 避免签发失败时目录被清空、面板失去可用证书。
        if [ ! -d "$certPath" ]; then
            mkdir -p "$certPath"
        fi
        LOGD "请设置域名:"
        read -r -p "Input your domain here:" CF_Domain
        LOGD "你的域名设置为:${CF_Domain}"
        LOGD "请设置API密钥:"
        read -r -p "Input your key here:" CF_GlobalKey
        LOGD "你的API密钥为:${CF_GlobalKey}"
        LOGD "请设置注册邮箱:"
        read -r -p "Input your email here:" CF_AccountEmail
        LOGD "你的注册邮箱为:${CF_AccountEmail}"
        if [[ -z "$CF_Domain" || -z "$CF_GlobalKey" || -z "$CF_AccountEmail" ]]; then
            LOGE "域名、API密钥、注册邮箱均不能为空,脚本退出"
            exit 1
        fi
        "$acme_sh" --set-default-ca --server letsencrypt
        if [ $? -ne 0 ]; then
            LOGE "修改默认CA为Lets'Encrypt失败,脚本退出"
            exit 1
        fi
        export CF_Key="${CF_GlobalKey}"
        export CF_Email="${CF_AccountEmail}"
        LOGI "正在通过 Cloudflare DNS 验证签发证书(域名: ${CF_Domain})..."
        # --force：同一域名已签过且未到期时 acme.sh 默认会跳过（Domains not changed），
        # 手动重跑本菜单理应重新签发，这里强制重签以覆盖旧证书。
        if ! "$acme_sh" --issue --dns dns_cf -d "${CF_Domain}" --log --force; then
            LOGE "证书签发失败,脚本退出"
            LOGI "详细日志请查看: ${acme_sh}.log"
            LOGI "可加 --debug 参数获取详细报错: $acme_sh --issue --dns dns_cf -d ${CF_Domain} --debug"
            exit 1
        else
            LOGI "证书签发成功,安装中..."
        fi
        # --reloadcmd：acme.sh 每次安装/续签证书后自动执行，重启面板以加载新证书。
        if ! "$acme_sh" --installcert -d "${CF_Domain}" --ca-file "${certPath}/ca.cer" \
            --cert-file "${certPath}/${CF_Domain}.cer" --key-file "${certPath}/${CF_Domain}.key" \
            --fullchain-file "${certPath}/fullchain.cer" \
            --reloadcmd "systemctl restart xui"; then
            LOGE "证书安装失败,脚本退出"
            exit 1
        else
            LOGI "证书安装成功,开启自动更新..."
        fi
        if ! "$acme_sh" --upgrade --auto-upgrade 2>/dev/null; then
            LOGE "自动更新(acme.sh --upgrade)设置失败,脚本退出"
            chmod 755 "$certPath"
            exit 1
        fi
        # 取消 acme.sh 自带的定时任务：续期改由下面的 systemd timer 调度，
        # 不再让 acme.sh 往 root 的 crontab 里写任务。
        "$acme_sh" --uninstall-cronjob >/dev/null 2>&1
        # 续期调度：每天检查一次，只有剩余有效期不足 7 天才触发重签；
        # 重签完成后 acme.sh 执行 --reloadcmd 自动重启面板，使新证书生效。
        cat > /etc/xui/renew-cert.sh <<'RENEW_EOF'
#!/bin/bash
#
# 面板证书续期检查，由 xui-ssl.timer 每天调用一次。
# 只有剩余有效期不足 7 天时才触发重签：acme.sh 自身的默认阈值是剩余不足 60 天
# （约到期前 30 天），直接执行 --cron 会提前约一个月就重签。
set -u
# 面板固定以 root 运行，这里不依赖 $HOME：systemd 服务不保证设置 HOME 变量。
cert="/root/cert/fullchain.cer"
acme_home="/root/.acme.sh"
acme_sh="$acme_home/acme.sh"
[ -f "$cert" ] || exit 0
[ -x "$acme_sh" ] || exit 0
# -checkend 604800 秒即 7 天：证书尚未进入 7 天窗口时直接退出。
openssl x509 -in "$cert" -noout -checkend 604800 && exit 0
"$acme_sh" --cron --home "$acme_home"
RENEW_EOF
        chmod 700 /etc/xui/renew-cert.sh
        cat > /etc/systemd/system/xui-ssl.service <<'SERVICE_EOF'
[Unit]
Description=Renew panel TLS certificate with acme.sh

[Service]
Type=oneshot
ExecStart=/etc/xui/renew-cert.sh
SERVICE_EOF
        cat > /etc/systemd/system/xui-ssl.timer <<'TIMER_EOF'
[Unit]
Description=Daily panel TLS certificate renewal check

[Timer]
OnCalendar=daily
Persistent=true

[Install]
WantedBy=timers.target
TIMER_EOF
        systemctl daemon-reload
        if systemctl enable --now xui-ssl.timer 2>/dev/null; then
            LOGI "已启用续期定时器 xui-ssl.timer(每天检查,剩余不足7天自动重签并重启面板)"
        else
            LOGE "续期定时器启用失败(证书已签发,可稍后手动执行 systemctl enable --now xui-ssl.timer)"
        fi
        LOGI "证书已安装并已开启自动续期"
        ls -lah "$certPath"
        chmod 755 "$certPath"
    else
        show_menu
    fi
}

show_usage() {
    echo "xui 管理脚本使用方法: "
    echo "------------------------------------------"
    echo "xui              - 显示管理菜单 (功能更多)"
    echo "xui start        - 启动 xui 面板"
    echo "xui stop         - 停止 xui 面板"
    echo "xui restart      - 重启 xui 面板"
    echo "xui status       - 查看 xui 状态"
    echo "xui enable       - 设置 xui 开机自启"
    echo "xui disable      - 取消 xui 开机自启"
    echo "xui log          - 查看 xui 日志"
    echo "xui v2-ui        - 迁移本机器的 v2-ui 账号数据至 xui"
    echo "xui update       - 更新 xui 面板"
    echo "xui install      - 安装 xui 面板"
    echo "xui uninstall    - 卸载 xui 面板"
    echo "------------------------------------------"
}

show_menu() {
    echo -e "
  ${green}xui 面板管理脚本${plain}
  ${green}0.${plain} 退出脚本
————————————————
  ${green}1.${plain} 安装 xui
  ${green}2.${plain} 更新 xui
  ${green}3.${plain} 升级 脚本
  ${green}4.${plain} 卸载 xui
————————————————
  ${green}5.${plain} 重置用户名密码
  ${green}6.${plain} 重置面板设置
  ${green}7.${plain} 设置面板端口
  ${green}8.${plain} 查看当前面板设置
————————————————
  ${green}9.${plain} 启动 xui
  ${green}10.${plain} 停止 xui
  ${green}11.${plain} 重启 xui
  ${green}12.${plain} 查看 xui 状态
  ${green}13.${plain} 查看 xui 日志
————————————————
  ${green}14.${plain} 设置 xui 开机自启
  ${green}15.${plain} 取消 xui 开机自启
————————————————
  ${green}16.${plain} 一键安装 bbr (最新内核)
  ${green}17.${plain} 一键申请SSL证书(acme申请)
 "
    show_status
    echo && read -p "请输入选择 [0-17]: " num

    case "${num}" in
    0)
        exit 0
        ;;
    1)
        check_uninstall && install
        ;;
    2)
        check_install && update
        ;;
    3)
        update_shell
        ;;
    4)
        check_install && uninstall
        ;;
    5)
        check_install && reset_user
        ;;
    6)
        check_install && reset_config
        ;;
    7)
        check_install && set_port
        ;;
    8)
        check_install && check_config
        ;;
    9)
        check_install && start
        ;;
    10)
        check_install && stop
        ;;
    11)
        check_install && restart
        ;;
    12)
        check_install && status
        ;;
    13)
        check_install && show_log
        ;;
    14)
        check_install && enable
        ;;
    15)
        check_install && disable
        ;;
    16)
        install_bbr
        ;;
    17)
        ssl_cert_issue
        ;;
    *)
        LOGE "请输入正确的数字 [0-17]"
        ;;
    esac
}

if [[ $# > 0 ]]; then
    case $1 in
    "start")
        check_install 0 && start 0
        ;;
    "stop")
        check_install 0 && stop 0
        ;;
    "restart")
        check_install 0 && restart 0
        ;;
    "status")
        check_install 0 && status 0
        ;;
    "enable")
        check_install 0 && enable 0
        ;;
    "disable")
        check_install 0 && disable 0
        ;;
    "log")
        check_install 0 && show_log 0
        ;;
    "v2-ui")
        check_install 0 && migrate_v2_ui 0
        ;;
    "update")
        check_install 0 && update 0
        ;;
    "install")
        check_uninstall 0 && install 0
        ;;
    "uninstall")
        check_install 0 && uninstall 0
        ;;
    *) show_usage ;;
    esac
else
    show_menu
fi
