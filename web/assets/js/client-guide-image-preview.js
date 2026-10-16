(function () {
    'use strict';

    Vue.component('client-guide-image-preview', {
        props: {
            src: {type:String, default:''},
            visible: {type:Boolean, default:false},
            alt: {type:String, default:''}
        },
        data() { return {actualSize:false, loadError:false}; },
        watch: {
            visible(value) { if (value) this.reset(); },
            src() { this.reset(); }
        },
        methods: {
            reset() { this.actualSize = false; this.loadError = false; },
            close() { this.$emit('close'); }
        },
        template: `
            <a-modal :visible="visible" title="图片原图" :width="1100" :z-index="1200"
                     :footer="null" :keyboard="true" :mask-closable="true"
                     wrap-class-name="client-guide-image-preview" @cancel="close">
                <div class="client-guide-image-toolbar">
                    <a-button size="small" :type="actualSize?'default':'primary'" @click="actualSize=false">适应窗口</a-button>
                    <a-button size="small" :type="actualSize?'primary':'default'" @click="actualSize=true">原始尺寸</a-button>
                </div>
                <div class="client-guide-image-viewport">
                    <p v-if="loadError" class="client-guide-image-error">图片加载失败，请检查图片地址后重试。</p>
                    <img v-if="visible&&src&&!loadError" :src="src" :alt="alt"
                         :class="{'client-guide-image-actual':actualSize}"
                         :title="actualSize?'点击适应窗口':'点击查看原始尺寸'"
                         @click="actualSize=!actualSize" @error="loadError=true">
                </div>
            </a-modal>`
    });
})();
