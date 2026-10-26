/**
 * 面板通知公告：公共工具 + 登录后弹窗。
 *
 * 加载于每个面板页面（见 common/js.html）。工具部分挂在 window.XuiAnnouncement 上，
 * 供「通知公告」历史页（announcements.html）复用同一套已读判定、转义与时间格式化。
 *
 * 已读同时记两处：
 *   1. localStorage（按公告 id + 更新时间），决定弹窗是否再出现；
 *   2. 服务端 xui/announcement/read，供管理员查看触达记录。
 *
 * 公告内容按纯文本渲染：先转义再识别 http(s) 链接，避免公告成为 XSS 入口。
 */
(function () {
    'use strict';

    var SEEN_KEY = 'xui.announcement.seen';
    var READ_ENDPOINT = 'xui/announcement/read';
    var LEVELS = {
        info: {text: '通知', color: 'blue'},
        warning: {text: '提醒', color: 'orange'},
        important: {text: '重要', color: 'red'},
    };

    function loadSeen() {
        try {
            var raw = window.localStorage.getItem(SEEN_KEY);
            var value = raw ? JSON.parse(raw) : {};
            return value && typeof value === 'object' ? value : {};
        } catch (e) {
            return {};
        }
    }

    function saveSeen(seen) {
        try {
            window.localStorage.setItem(SEEN_KEY, JSON.stringify(seen));
        } catch (e) {
            // 隐私模式下 localStorage 不可写，退化为每次登录都提示，不影响功能。
        }
    }

    function escapeHtml(text) {
        return String(text).replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#39;');
    }

    // 先切分链接再逐段转义：链接本身不会被二次解析，文本也不会被当成 HTML。
    function contentHtml(text) {
        var raw = String(text == null ? '' : text);
        var pattern = /https?:\/\/[^\s<>"']+/g;
        var html = '';
        var last = 0;
        var match;
        while ((match = pattern.exec(raw))) {
            html += escapeHtml(raw.slice(last, match.index));
            var url = escapeHtml(match[0]);
            html += '<a href="' + url + '" target="_blank" rel="noopener noreferrer">' + url + '</a>';
            last = match.index + match[0].length;
        }
        html += escapeHtml(raw.slice(last));
        return html.replace(/\n/g, '<br>');
    }

    function levelOf(level) {
        return LEVELS[level] || LEVELS.info;
    }

    function formatTime(seconds) {
        return seconds ? new Date(seconds * 1000).toLocaleString() : '-';
    }

    function versionOf(item) {
        return Number(item && item.updatedAt) || 0;
    }

    function isUnread(item) {
        if (!item || !item.id) {
            return false;
        }
        var seen = loadSeen();
        return !(Number(seen[item.id]) >= versionOf(item));
    }

    // 服务端记录只做尽力而为：失败不影响本地已读状态。
    // 用 fetch + keepalive：点「查看全部公告」会立刻跳转，普通 XHR 会被页面卸载中断。
    function reportRead(ids) {
        if (!ids || ids.length === 0) {
            return;
        }
        var body = JSON.stringify({ids: ids});
        var url = basePath + READ_ENDPOINT;
        if (window.fetch) {
            try {
                fetch(url, {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json',
                        'X-Requested-With': 'XMLHttpRequest',
                    },
                    credentials: 'same-origin',
                    body: body,
                    keepalive: true,
                }).catch(function () {
                });
                return;
            } catch (e) {
                // 老浏览器不支持 keepalive 时退回 axios。
            }
        }
        try {
            axios.post(url, body, {
                headers: {'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest'},
                timeout: 8000,
            }).catch(function () {
            });
        } catch (e) {
            // 网络异常时忽略，下次打开还会继续提示。
        }
    }

    function markRead(items) {
        if (!items || items.length === 0) {
            return;
        }
        var seen = loadSeen();
        var ids = [];
        items.forEach(function (item) {
            if (!item || !item.id) {
                return;
            }
            seen[item.id] = versionOf(item);
            ids.push(item.id);
        });
        saveSeen(seen);
        reportRead(ids);
    }

    function unread(items) {
        return (items || []).filter(isUnread);
    }

    window.XuiAnnouncement = {
        isUnread: isUnread,
        markRead: markRead,
        levelOf: levelOf,
        contentHtml: contentHtml,
        formatTime: formatTime,
        unread: unread,
    };

    // 历史公告页自己就是公告列表，不再叠一层弹窗；登录页没有侧边栏，直接跳过。
    if (window.__xuiAnnouncementPage || !document.getElementById('sider') || typeof Vue === 'undefined') {
        return;
    }

    function injectStyle() {
        if (document.getElementById('xui-announcement-style')) {
            return;
        }
        var style = document.createElement('style');
        style.id = 'xui-announcement-style';
        style.textContent = [
            '.xui-announcement-list{max-height:60vh;overflow-y:auto}',
            '.xui-announcement-item{padding:8px 0;border-bottom:1px solid #f0f0f0}',
            '.xui-announcement-item:last-child{border-bottom:none}',
            '.xui-announcement-head{display:flex;align-items:center;gap:8px;margin-bottom:6px}',
            '.xui-announcement-head strong{font-size:15px;color:rgba(0,0,0,.85);word-break:break-word}',
            '.xui-announcement-body{color:rgba(0,0,0,.65);line-height:1.65;white-space:normal;word-break:break-word}',
            '.xui-announcement-time{margin-top:6px;font-size:12px;color:rgba(0,0,0,.45)}',
        ].join('');
        document.head.appendChild(style);
    }

    function show(items) {
        injectStyle();
        var host = document.createElement('div');
        document.body.appendChild(host);
        new Vue({
            el: host,
            delimiters: ['[[', ']]'],
            template: [
                '<a-modal :visible="visible" title="通知公告" :width="560" :mask-closable="false"',
                '         :footer="null" @cancel="close">',
                '  <div class="xui-announcement-list">',
                '    <div class="xui-announcement-item" v-for="item in items" :key="item.id">',
                '      <div class="xui-announcement-head">',
                '        <a-tag :color="levelOf(item.level).color">[[ levelOf(item.level).text ]]</a-tag>',
                '        <strong>[[ item.title ]]</strong>',
                '      </div>',
                '      <div class="xui-announcement-body" v-html="body(item.content)"></div>',
                '      <div class="xui-announcement-time" v-if="item.createdAt">',
                '        [[ time(item.createdAt) ]]',
                '      </div>',
                '    </div>',
                '  </div>',
                '  <div style="display:flex;align-items:center;justify-content:space-between;margin-top:18px">',
                '    <a-button type="link" style="padding-left:0" @click="openAll">查看全部公告</a-button>',
                '    <a-button type="primary" @click="close">我知道了</a-button>',
                '  </div>',
                '</a-modal>',
            ].join('\n'),
            data: {
                visible: true,
                items: items,
            },
            methods: {
                levelOf: levelOf,
                body: contentHtml,
                time: formatTime,
                openAll: function () {
                    markRead(this.items);
                    this.visible = false;
                    location.href = basePath + 'xui/announcements';
                },
                close: function () {
                    markRead(this.items);
                    this.visible = false;
                    var self = this;
                    var rendered = self.$el;
                    setTimeout(function () {
                        self.$destroy();
                        // Vue 用渲染结果替换了挂载点，所以按渲染根节点回收，
                        // 同时兜底清理原始挂载点，避免残留空节点。
                        if (rendered && rendered.parentNode) {
                            rendered.parentNode.removeChild(rendered);
                        }
                        if (host && host.parentNode) {
                            host.parentNode.removeChild(host);
                        }
                    }, 200);
                },
            },
        });
    }

    function load() {
        axios.get(basePath + 'xui/announcement/active', {timeout: 8000}).then(function (resp) {
            var data = resp && resp.data;
            if (!data || !data.success || !Array.isArray(data.obj)) {
                return;
            }
            var items = unread(data.obj);
            if (items.length > 0) {
                show(items);
            }
        }).catch(function () {
            // 公告属于附加提示，网络或会话异常时不打扰用户。
        });
    }

    if (document.readyState === 'complete') {
        load();
    } else {
        window.addEventListener('load', load);
    }
})();
