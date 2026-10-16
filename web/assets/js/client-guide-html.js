(function () {
    'use strict';

    const anchorPattern = /^[\p{L}\p{N}_.:-]{1,128}$/u;

    function anchorName(value) {
        return typeof value === 'string' && anchorPattern.test(value) ? value : '';
    }

    function fragmentName(value) {
        if (typeof value !== 'string' || !value.startsWith('#')) return '';
        try { return anchorName(decodeURIComponent(value.slice(1))); }
        catch (e) { return ''; }
    }

    function httpURL(value) {
        try {
            const url = new URL(String(value).trim());
            return (url.protocol === 'https:' || url.protocol === 'http:') && !url.username && !url.password ? url.href : '';
        } catch (e) { return ''; }
    }

    function linkURL(value) {
        const raw = String(value || '').trim();
        if (!raw.startsWith('#')) return httpURL(raw);
        const name = fragmentName(raw);
        return name ? '#' + name : '';
    }

    function escapeText(value) {
        return String(value || '').replace(/[&<>"']/g, character => ({'&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;'}[character]));
    }

    function sanitize(value) {
        if (!window.DOMPurify) return escapeText(value);
        // Preserve id/name only in this detached fragment, then remove them before
        // any content enters the panel DOM so authored anchors cannot shadow its IDs.
        const fragment = DOMPurify.sanitize(String(value || ''), {
            USE_PROFILES: {html:true},
            RETURN_DOM_FRAGMENT: true,
            SANITIZE_DOM: false,
            ADD_ATTR: ['data-guide-anchor'],
            FORBID_TAGS: ['script','style','form','input','button','select','option','textarea','iframe','object','embed','svg','math','link','meta','base'],
            FORBID_ATTR: ['srcset','formaction','srcdoc']
        });
        fragment.querySelectorAll('*').forEach(element => {
            const name = anchorName(element.getAttribute('id')) ||
                (element.tagName === 'A' ? anchorName(element.getAttribute('name')) : '') || anchorName(element.getAttribute('data-guide-anchor'));
            element.removeAttribute('id');
            element.removeAttribute('name');
            element.removeAttribute('data-guide-anchor');
            if (name) element.setAttribute('data-guide-anchor', name);
        });
        fragment.querySelectorAll('img').forEach(image => {
            const src = image.getAttribute('src') || '';
            if (!httpURL(src) && !/^data:image\/(?:png|jpeg|jpg|gif|webp);base64,[a-z0-9+/=\s]+$/i.test(src)) image.remove();
        });
        fragment.querySelectorAll('a').forEach(link => {
            const href = linkURL(link.getAttribute('href'));
            if (href) link.setAttribute('href', href);
            else link.removeAttribute('href');
            if (!href || href.startsWith('#')) link.removeAttribute('target');
            link.setAttribute('rel', Array.from(new Set((link.getAttribute('rel') || '').split(/\s+/).filter(Boolean).concat(['noopener','noreferrer']))).join(' '));
        });
        const container = document.createElement('div');
        container.appendChild(fragment);
        return container.innerHTML;
    }

    function handleAnchorClick(event, container) {
        const element = event.target && (event.target.nodeType === 1 ? event.target : event.target.parentElement);
        const link = element && element.closest('a[href]');
        if (!link || !container || !container.contains(link)) return false;
        const href = link.getAttribute('href') || '';
        if (!href.startsWith('#')) return false;
        event.preventDefault();
        const name = fragmentName(href);
        if (name) {
            const target = Array.from(container.querySelectorAll('[data-guide-anchor]')).find(item => item.getAttribute('data-guide-anchor') === name);
            if (target) {
                // Scroll only the nearest guide/Ant modal scroller, leaving the
                // panel behind the modal and its URL unchanged.
                for (let scroller = target.parentElement; scroller && scroller !== document.body && scroller !== document.documentElement; scroller = scroller.parentElement) {
                    if (/^(auto|scroll|overlay)$/.test(getComputedStyle(scroller).overflowY) && scroller.scrollHeight > scroller.clientHeight) {
                        const top = scroller.scrollTop + target.getBoundingClientRect().top - scroller.getBoundingClientRect().top - scroller.clientTop;
                        scroller.scrollTo({top:Math.max(0,top), behavior:'smooth'});
                        break;
                    }
                }
            }
        }
        return true;
    }

    window.ClientGuideHTML = {sanitize, linkURL, handleAnchorClick};
})();
