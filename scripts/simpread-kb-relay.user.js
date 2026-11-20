// ==UserScript==
// @name         简悦 → 知识库中转（云端 API）
// @namespace    local.kb-relay.cloud
// @version      2.0.0
// @description  在简悦阅读模式下，先按 s r 进入快捷键操作模式，再按 z z 把当前文章提交到云端知识库中转站
// @author       local
// @match        *://*/*
// @grant        GM_xmlhttpRequest
// @connect      relay.example.com
// @run-at       document-idle
// ==/UserScript==

(function () {
  'use strict';

  // 公网中转接口；令牌对应服务器 nginx 的 X-Ingest-Key
  const ENDPOINT = 'https://relay.example.com/ingest/simpread';
  const INGEST_KEY = '换成你自己的随机串'; // 需与反向代理里的 X-Ingest-Key 一致

  // 两键序列的有效间隔（毫秒）
  const SEQ_TIMEOUT = 1000;

  let armed = false;      // 是否处于“快捷键操作模式”
  let seq = '';           // 当前按键序列
  let lastKeyAt = 0;      // 上一次按键时间
  let seqTimer = 0;

  function isReaderActive() {
    const root = document.querySelector('.simpread-read-root');
    if (!root) return false;
    if (root.classList.contains('simpread-read-root-show')) return true;
    const rect = root.getBoundingClientRect();
    return rect.height > 0 && rect.top > -100;
  }

  function resetSeq() {
    seq = '';
    clearTimeout(seqTimer);
  }

  function toast(text, ms) {
    ms = ms || 3500;
    let el = document.getElementById('kb-relay-toast');
    if (!el) {
      el = document.createElement('div');
      el.id = 'kb-relay-toast';
      el.style.cssText = [
        'position:fixed',
        'right:18px',
        'bottom:18px',
        'z-index:2147483647',
        'max-width:360px',
        'padding:10px 14px',
        'border-radius:8px',
        'background:rgba(17,24,39,.92)',
        'color:#fff',
        'font:13px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif',
        'box-shadow:0 6px 24px rgba(0,0,0,.25)',
        'white-space:pre-wrap',
        'word-break:break-word',
        'pointer-events:none',
        'opacity:0',
        'transition:opacity .2s'
      ].join(';');
      document.documentElement.appendChild(el);
    }
    el.textContent = text;
    requestAnimationFrame(function () {
      el.style.opacity = '1';
    });
    clearTimeout(el._kbTimer);
    el._kbTimer = setTimeout(function () {
      el.style.opacity = '0';
      setTimeout(function () {
        if (el.parentNode) el.parentNode.removeChild(el);
      }, 250);
    }, ms);
  }

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  // 把图片/链接的相对地址转成绝对地址；顺便把懒加载图片的 data-src 提上来
  function absolutize(clone) {
    clone.querySelectorAll('script,style,noscript,link,meta').forEach(function (n) {
      n.remove();
    });
    clone.querySelectorAll('img').forEach(function (img) {
      const lazy = img.getAttribute('data-src') ||
                   img.getAttribute('data-original') ||
                   img.getAttribute('data-actualsrc');
      let src = img.getAttribute('src') || '';
      if ((!src || src.indexOf('data:') === 0) && lazy) src = lazy;
      if (src && !/^(https?:|data:)/i.test(src)) {
        try { src = new URL(src, location.href).href; } catch (e) {}
      }
      if (src) img.setAttribute('src', src);
      else img.removeAttribute('src');
    });
    clone.querySelectorAll('a[href]').forEach(function (a) {
      const href = a.getAttribute('href');
      if (href && !/^(https?:|mailto:|#)/i.test(href)) {
        try { a.setAttribute('href', new URL(href, location.href).href); } catch (e) {}
      }
    });
  }

  function buildPayload() {
    const root = document.querySelector('.simpread-read-root');
    if (!root) return null;

    const titleEl = root.querySelector('sr-rd-title');
    const contentEl = root.querySelector('sr-rd-content');
    if (!contentEl) return null;

    const title = ((titleEl && titleEl.textContent) || document.title || '').trim() || '未命名';
    const clone = contentEl.cloneNode(true);
    absolutize(clone);

    // 中转站的 /ingest/simpread 只有 content 以 <!doctype 或 <html 开头时才按 HTML 解析
    const html = '<!doctype html><html><head><meta charset="utf-8"><title>' +
      escapeHtml(title) + '</title></head><body>' + clone.innerHTML + '</body></html>';

    const canonical = document.querySelector('link[rel="canonical"]');
    let url = (canonical && canonical.href) ? canonical.href : location.href;
    url = url.replace(/([?&])simpread_mode=read.*$/i, '').replace(/#.*$/, '');

    return { url: url, title: title, content: html };
  }

  function send() {
    const payload = buildPayload();
    if (!payload) {
      toast('未找到简悦阅读模式正文，请先进入阅读模式。');
      return;
    }

    toast('正在提交到知识库中转…');

    function done(ok, msg) {
      toast(ok ? msg : ('提交失败：' + msg), ok ? 4000 : 6000);
    }

    if (typeof GM_xmlhttpRequest !== 'function') {
      done(false, '当前浏览器未启用 GM_xmlhttpRequest，请在 Tampermonkey 中启用后重试。');
      return;
    }

    function post(url, headers, next) {
      GM_xmlhttpRequest({
        method: 'POST',
        url: url,
        headers: headers,
        data: JSON.stringify(payload),
        timeout: 20000,
        onload: function (res) {
          if (res.status >= 200 && res.status < 300) {
            let file = '';
            try { file = JSON.parse(res.responseText).file || ''; } catch (e) {}
            done(true, file ? ('已提交：' + file) : '已提交到知识库中转。');
          } else if (res.status === 401) {
            done(false, '接口令牌无效，请检查脚本里的 X-Ingest-Key。');
          } else if (next && (res.status >= 500 || res.status === 429)) {
            next();
          } else {
            done(false, 'HTTP ' + res.status + ' ' + String(res.responseText || '').slice(0, 120));
          }
        },
        onerror: function () {
          if (next) next(); else done(false, '无法连接中转接口。');
        },
        ontimeout: function () {
          if (next) next(); else done(false, '请求超时，请稍后重试。');
        }
      });
    }

    const keyHeaders = { 'Content-Type': 'application/json', 'X-Ingest-Key': INGEST_KEY };
    post(ENDPOINT, keyHeaders, function () {
      done(false, '无法连接中转接口，请检查域名与网络后重试。');
    });
  }

  function onKeyDown(e) {
    if (e.repeat || e.ctrlKey || e.metaKey || e.altKey) return;

    const t = e.target;
    if (t && t.closest && t.closest('input,textarea,select,[contenteditable="true"],[contenteditable=""]')) {
      return;
    }

    const k = String(e.key || '').toLowerCase();
    if (!/^[a-z]$/.test(k)) {
      resetSeq();
      return;
    }

    if (!isReaderActive()) {
      armed = false;
      resetSeq();
      return;
    }

    const now = Date.now();
    if (seq && now - lastKeyAt > SEQ_TIMEOUT) seq = '';
    lastKeyAt = now;
    seq += k;
    clearTimeout(seqTimer);
    seqTimer = setTimeout(function () { seq = ''; }, SEQ_TIMEOUT);

    // s r：镜像简悦原生的“快捷键操作模式”开关
    if (seq === 'sr') {
      armed = !armed;
      resetSeq();
      toast(armed ? '已进入中转站快捷键模式：按 z z 导入。' : '已退出中转站快捷键模式。', 2200);
      return;
    }

    // z z：导入
    if (seq === 'zz') {
      resetSeq();
      if (!armed) {
        toast('请先按 s r 进入快捷键操作模式。', 2200);
        return;
      }
      e.preventDefault();
      send();
      return;
    }

    if (seq.length > 2) seq = seq.slice(-2);
  }

  window.addEventListener('keydown', onKeyDown, true);
})();
