(function () {
    'use strict';

    const imageTypes = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'];
    const imageLimit = 5 * 1024 * 1024;

    function httpURL(value) {
        try {
            const url = new URL(String(value).trim());
            return (url.protocol === 'https:' || url.protocol === 'http:') && !url.username && !url.password ? url.href : '';
        } catch (e) { return ''; }
    }

    function escapeText(value) {
        return String(value).replace(/[&<>"']/g, character => ({'&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;'}[character]));
    }

    function cleanHTML(value) {
        return window.ClientGuideHTML ? ClientGuideHTML.sanitize(value) : escapeText(value);
    }

    Vue.component('client-guide-editor', {
        delimiters: ['[[', ']]'],
        props: {
            value: {type:String, default:''},
            disabled: {type:Boolean, default:false}
        },
        data() {
            return {mode:'visual', sourceHTML:'', previewHTML:'', imageBusy:false, imageDialog:false, imageURL:'', linkDialog:false, linkURL:'', linkText:'', color:'#333333'};
        },
        computed: {
            formattingDisabled() { return this.disabled || this.imageBusy || this.mode !== 'visual'; }
        },
        template: `
            <div class="client-guide-editor">
                <div class="client-guide-toolbar">
                    <select aria-label="标题" :disabled="formattingDisabled" @mousedown="rememberSelection" @change="format('formatBlock',$event.target.value);$event.target.value=''">
                        <option value="">正文 / 标题</option><option value="p">正文</option><option value="h1">一级标题</option><option value="h2">二级标题</option><option value="h3">三级标题</option><option value="blockquote">引用</option><option value="pre">代码段</option>
                    </select>
                    <button type="button" title="粗体" :disabled="formattingDisabled" @mousedown.prevent="rememberSelection" @click="format('bold')"><b>粗</b></button>
                    <button type="button" title="斜体" :disabled="formattingDisabled" @mousedown.prevent="rememberSelection" @click="format('italic')"><i>斜</i></button>
                    <button type="button" title="下划线" :disabled="formattingDisabled" @mousedown.prevent="rememberSelection" @click="format('underline')"><u>下划线</u></button>
                    <select aria-label="字号" :disabled="formattingDisabled" @mousedown="rememberSelection" @change="format('fontSize',$event.target.value);$event.target.value=''">
                        <option value="">字号</option><option value="2">小</option><option value="3">标准</option><option value="4">大</option><option value="5">较大</option><option value="6">特大</option><option value="7">最大</option>
                    </select>
                    <label>颜色<input type="color" v-model="color" :disabled="formattingDisabled" @mousedown="rememberSelection" @change="format('foreColor',color)"></label>
                    <button type="button" :disabled="formattingDisabled" @mousedown.prevent="rememberSelection" @click="format('justifyLeft')">左对齐</button>
                    <button type="button" :disabled="formattingDisabled" @mousedown.prevent="rememberSelection" @click="format('justifyCenter')">居中</button>
                    <button type="button" :disabled="formattingDisabled" @mousedown.prevent="rememberSelection" @click="format('justifyRight')">右对齐</button>
                    <button type="button" :disabled="formattingDisabled" @mousedown.prevent="rememberSelection" @click="format('insertUnorderedList')">项目列表</button>
                    <button type="button" :disabled="formattingDisabled" @mousedown.prevent="rememberSelection" @click="format('insertOrderedList')">编号列表</button>
                    <button type="button" :disabled="formattingDisabled" @mousedown.prevent="rememberSelection" @click="openLinkDialog">链接</button>
                    <button type="button" :disabled="formattingDisabled" @mousedown.prevent="rememberSelection" @click="openImageDialog">插图</button>
                    <button type="button" :disabled="formattingDisabled" @mousedown.prevent="rememberSelection" @click="format('undo')">撤销</button>
                    <button type="button" :disabled="formattingDisabled" @mousedown.prevent="rememberSelection" @click="format('redo')">重做</button>
                    <div class="client-guide-mode">
                        <button type="button" :class="{'is-active':mode==='visual'}" :disabled="disabled||imageBusy" @click="changeMode('visual')">可视化</button>
                        <button type="button" :class="{'is-active':mode==='source'}" :disabled="disabled||imageBusy" @click="changeMode('source')">HTML 源码</button>
                        <button type="button" :class="{'is-active':mode==='preview'}" :disabled="disabled||imageBusy" @click="changeMode('preview')">预览</button>
                    </div>
                </div>
                <div v-show="mode==='visual'" ref="editor" class="client-guide-editor-body client-guide-content" :contenteditable="disabled||imageBusy?'false':'true'" role="textbox" aria-label="使用说明正文" aria-multiline="true" data-placeholder="在这里编写使用说明，可插入图片、链接和格式化文字……" @input="visualInput" @keyup="rememberSelection" @mouseup="rememberSelection" @blur="rememberSelection" @paste="pasteContent" @drop="dropContent" @dragover.prevent></div>
                <textarea v-if="mode==='source'" class="client-guide-source" :value="sourceHTML" :disabled="disabled" aria-label="使用说明 HTML 源码" spellcheck="false" @input="sourceInput($event.target.value)"></textarea>
                <div v-if="mode==='preview'" class="client-guide-preview client-guide-content" @click="handleAnchorClick" v-html="previewHTML"></div>
                <a-modal v-model="linkDialog" title="插入链接" :footer="null" :width="460">
                    <div class="client-guide-insert-row"><label>链接地址（HTTP / HTTPS 或文内锚点）</label><a-input v-model.trim="linkURL" placeholder="https://example.com/ 或 #install" @pressEnter="insertLink"></a-input></div>
                    <div class="client-guide-insert-row"><label>显示文字</label><a-input v-model="linkText" placeholder="留空使用所选文字或链接地址"></a-input></div>
                    <a-button type="primary" @click="insertLink">插入链接</a-button>
                </a-modal>
                <a-modal v-model="imageDialog" title="插入图片" :footer="null" :width="480">
                    <a-spin :spinning="imageBusy" tip="正在插入图片">
                        <div class="client-guide-insert-row"><label>选择本地图片，可多选</label><input type="file" accept="image/png,image/jpeg,image/gif,image/webp" multiple :disabled="imageBusy" @change="selectImages($event)"><p>PNG、JPG、GIF、WebP，每张最大 5MiB。</p></div>
                        <a-divider>或使用图片地址</a-divider>
                        <div class="client-guide-insert-row"><a-input v-model.trim="imageURL" placeholder="https://example.com/image.png" :disabled="imageBusy" @pressEnter="insertImageURL"></a-input></div>
                        <a-button type="primary" :disabled="imageBusy" @click="insertImageURL">插入网络图片</a-button>
                    </a-spin>
                </a-modal>
            </div>`,
        watch: {
            value(value) {
                if (this.mode === 'source') this.sourceHTML = value || '';
                if (this.mode === 'preview') this.previewHTML = cleanHTML(value);
                const editor = this.$refs.editor;
                // Normal typing already changed this DOM; do not replace it or move the caret.
                if (this.mode === 'visual' && editor && editor.innerHTML !== value) {
                    editor.innerHTML = cleanHTML(value);
                    this.savedSelection = null;
                }
            }
        },
        mounted() {
            this.$refs.editor.innerHTML = cleanHTML(this.value);
            this.sourceHTML = this.value;
            this.savedSelection = null;
        },
        methods: {
            handleAnchorClick(event) {
                ClientGuideHTML.handleAnchorClick(event, event.currentTarget);
            },
            rememberSelection() {
                const editor = this.$refs.editor;
                const selection = window.getSelection();
                if (!selection || !selection.rangeCount || !editor) return;
                const range = selection.getRangeAt(0);
                if (editor.contains(range.commonAncestorContainer)) this.savedSelection = range.cloneRange();
            },
            restoreSelection() {
                const editor = this.$refs.editor;
                editor.focus();
                const selection = window.getSelection();
                const range = this.savedSelection;
                if (range && editor.contains(range.commonAncestorContainer)) {
                    selection.removeAllRanges();
                    selection.addRange(range);
                } else {
                    const end = document.createRange();
                    end.selectNodeContents(editor);
                    end.collapse(false);
                    selection.removeAllRanges();
                    selection.addRange(end);
                }
            },
            visualInput() {
                this.rememberSelection();
                this.$emit('input', this.$refs.editor.innerHTML);
            },
            sourceInput(value) {
                this.sourceHTML = value;
                this.$emit('input', value);
            },
            format(command, value) {
                if (this.formattingDisabled) return;
                this.restoreSelection();
                document.execCommand('styleWithCSS', false, true);
                document.execCommand(command, false, value === undefined ? null : value);
                this.visualInput();
            },
            changeMode(mode) {
                if (this.disabled || this.imageBusy || mode === this.mode) return;
                const html = this.mode === 'source' ? this.sourceHTML : this.value;
                const clean = cleanHTML(html);
                if (mode === 'source') this.sourceHTML = html;
                if (mode === 'preview') this.previewHTML = clean;
                this.mode = mode;
                if (mode === 'visual') {
                    this.$refs.editor.innerHTML = clean;
                    this.savedSelection = null;
                    this.$emit('input', clean);
                }
            },
            insertHTML(html) {
                this.restoreSelection();
                document.execCommand('insertHTML', false, cleanHTML(html));
                this.visualInput();
            },
            openLinkDialog() {
                this.rememberSelection();
                this.linkURL = '';
                const selection = window.getSelection();
                this.linkText = selection ? selection.toString() : '';
                this.linkDialog = true;
            },
            insertLink() {
                const url = ClientGuideHTML.linkURL(this.linkURL);
                if (!url) { this.$message.error('请输入 HTTP / HTTPS 链接或有效的 #文内锚点'); return; }
                const target = url.startsWith('#') ? '' : ' target="_blank" rel="noopener noreferrer"';
                this.linkDialog = false;
                this.$nextTick(() => this.insertHTML('<a href="' + escapeText(url) + '"' + target + '>' + escapeText(this.linkText || url) + '</a>'));
            },
            openImageDialog() {
                this.rememberSelection();
                this.imageURL = '';
                this.imageDialog = true;
            },
            insertImageURL() {
                if (this.imageBusy) return;
                const url = httpURL(this.imageURL);
                if (!url) { this.$message.error('请输入完整的 HTTP 或 HTTPS 图片地址'); return; }
                this.imageDialog = false;
                this.$nextTick(() => this.insertHTML('<img src="' + escapeText(url) + '" alt="图片"><p><br></p>'));
            },
            async selectImages(event) {
                const files = Array.from(event.target.files || []);
                event.target.value = '';
                await this.insertImages(files);
            },
            async insertImages(files) {
                if (this.disabled || this.imageBusy || !files.length) return;
                for (const file of files) {
                    if (!imageTypes.includes(file.type)) { this.$message.error('仅支持 PNG、JPG、GIF、WebP 图片'); return; }
                    if (file.size > imageLimit) { this.$message.error('每张图片最大 5MiB：' + file.name); return; }
                }
                // Keep the editing position while FileReader and the image dialog take focus.
                this.rememberSelection();
                this.imageBusy = true;
                this.$emit('busy', true);
                try {
                    const images = [];
                    for (const file of files) {
                        const src = await new Promise((resolve, reject) => {
                            const reader = new FileReader();
                            reader.onload = () => resolve(reader.result);
                            reader.onerror = () => reject(new Error('读取图片失败：' + file.name));
                            reader.readAsDataURL(file);
                        });
                        images.push('<img src="' + escapeText(src) + '" alt="' + escapeText(file.name) + '"><p><br></p>');
                    }
                    this.imageDialog = false;
                    this.imageBusy = false;
                    await this.$nextTick();
                    this.insertHTML(images.join(''));
                } catch (e) { this.$message.error(e.message); }
                finally { this.imageBusy = false; this.$emit('busy', false); }
            },
            insertTransfer(transfer) {
                const files = Array.from(transfer.files || []);
                if (files.length) { this.insertImages(files); return; }
                const html = transfer.getData('text/html');
                const text = transfer.getData('text/plain');
                if (html || text) this.insertHTML(html || escapeText(text).replace(/\r?\n/g, '<br>'));
            },
            pasteContent(event) {
                event.preventDefault();
                if (this.disabled || this.imageBusy || !event.clipboardData) return;
                this.rememberSelection();
                this.insertTransfer(event.clipboardData);
            },
            dropContent(event) {
                event.preventDefault();
                if (this.disabled || this.imageBusy || !event.dataTransfer) return;
                const range = document.caretRangeFromPoint ? document.caretRangeFromPoint(event.clientX, event.clientY) : null;
                if (range && this.$refs.editor.contains(range.commonAncestorContainer)) {
                    this.savedSelection = range;
                    const selection = window.getSelection();
                    selection.removeAllRanges();
                    selection.addRange(range);
                }
                this.insertTransfer(event.dataTransfer);
            }
        }
    });
})();
