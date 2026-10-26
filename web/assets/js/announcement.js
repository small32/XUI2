/**
 * 面板通知公告：公共工具 + 登录后弹窗。
 *
 * 加载于每个面板页面（见 common/js.html）。工具部分挂在 window.XuiAnnouncement 上，
 * 供「通知公告」历史页（announcements.html）复用同一套已读判定、转义与时间格式化。
 *
 * 已读由服务端按账号判定（announcement/active 返回每条的 read），浏览器本地不留存
 * 记录：同一个浏览器先登录过的账号不会把公告"替下一个账号读掉"，换设备也不会重复弹。
 * 用户在弹窗上确认后才调 announcement/read 上报，网络失败时保留未读，下次登录继续提醒。
 *
 * 公告内容按纯文本渲染：先转义再识别 http(s) 链接，避免公告成为 XSS 入口。
 */
(function () {
    'use strict';

    var READ_ENDPOINT = 'xui/announcement/read';
    var LEVELS = {
        info: {text: '通知', color: 'blue'},
        warning: {text: '提醒', color: 'orange'},
        important: {text: '重要', color: 'red'},
    };

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

    // 已读跟着账号走，由服务端在 active/history 响应里给出，前端不再本地缓存。
    function isUnread(item) {
        return !!(item && item.id) && item.read !== true;
    }

    // 上报只做尽力而为：失败时回调会收到 false，调用方可据此把界面恢复成未读，
    // 下次打开还会继续提示，不会悄悄丢掉一次公告提醒。
    // 用 fetch + keepalive：点「查看全部公告」会立刻跳转，普通 XHR 会被页面卸载中断。
    function reportRead(ids, callback) {
        if (!ids || ids.length === 0) {
            return;
        }
        var body = JSON.stringify({ids: ids});
        var url = basePath + READ_ENDPOINT;
        var done = function (ok) {
            if (callback) {
                callback(ok);
            }
        };
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
                }).then(function (resp) {
                    done(!!(resp && resp.ok));
                }).catch(function () {
                    done(false);
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
            }).then(function (resp) {
                done(!!(resp && resp.data && resp.data.success));
            }).catch(function () {
                done(false);
            });
        } catch (e) {
            done(false);
        }
    }

    // 乐观地把界面标成已读，上报失败再退回未读——两者都以服务端为准。
    function markRead(items) {
        if (!items || items.length === 0) {
            return;
        }
        var ids = [];
        items.forEach(function (item) {
            if (!item || !item.id) {
                return;
            }
            item.read = true;
            ids.push(item.id);
        });
        reportRead(ids, function (ok) {
            if (!ok) {
                items.forEach(function (item) {
                    if (item && item.id) {
                        item.read = false;
                    }
                });
            }
        });
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
