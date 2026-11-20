'use strict';

const byId = id => document.getElementById(id);
const queue = byId('batch-form');
const terms = byId('terms-form');
const splitReview = matchMedia('(min-width: 821px) and (min-height: 500px)');
const review = document.querySelector('.review-layout');

for (const button of document.querySelectorAll('[data-open-import]')) {
  button.addEventListener('click', () => byId('import-dialog').showModal());
}
for (const select of document.querySelectorAll('[data-submit-filter]')) {
  select.addEventListener('change', () => select.form.requestSubmit());
}

const batchChecks = queue ? [...queue.querySelectorAll('.doc-check')] : [];
function updateSelection() {
  const count = batchChecks.filter(check => check.checked).length;
  byId('batch-toolbar').hidden = count === 0;
  byId('selection-count').textContent = '已选 ' + count + ' 篇';
}
if (queue) {
  queue.addEventListener('change', updateSelection);
  window.addEventListener('pageshow', updateSelection);
  for (const button of document.querySelectorAll('[data-select-page], [data-clear-selection]')) {
    button.addEventListener('click', () => {
      const checked = button.hasAttribute('data-select-page');
      for (const check of batchChecks) check.checked = checked;
      updateSelection();
      if (!checked) document.querySelector('[data-select-page]')?.focus();
    });
  }
  for (const image of queue.querySelectorAll('.row-scene img')) {
    const removeScene = () => {
      image.closest('.article-window').classList.remove('has-scene');
      image.parentElement.remove();
    };
    image.addEventListener('error', removeScene, {once: true});
    if (image.complete && !image.naturalWidth) removeScene();
  }
}

document.addEventListener('submit', event => {
  const form = event.target;
  if (form === queue && !batchChecks.some(check => check.checked)) {
    event.preventDefault();
    return;
  }
  const confirmations = {
    purge: '永久删除所选的本地回收站记录？',
    trash: '将所选文章移入回收站？',
  };
  const message = form === queue ? confirmations[byId('batch-action').value] :
    event.submitter?.dataset.confirm || form.dataset.confirm;
  if (message && !window.confirm(message)) event.preventDefault();
});

const batchProgress = byId('batch-progress');
if (batchProgress) {
  let busy = Number(batchProgress.dataset.writing) > 0 || Boolean(batchProgress.dataset.batch);
  let dismissed = false;
  const bar = byId('batch-bar');
  const title = byId('batch-title');
  const meta = byId('batch-meta');
  const detail = byId('batch-detail');
  const dismiss = byId('dismiss-progress');
  const refresh = document.createElement('button');
  refresh.type = 'button';
  refresh.className = 'text-button';
  refresh.textContent = '刷新列表';
  refresh.hidden = true;
  dismiss.before(refresh);
  refresh.addEventListener('click', () => location.reload());
  dismiss.addEventListener('click', () => {
    batchProgress.hidden = true;
    dismissed = true;
  });
  async function pollProgress() {
    let current = false;
    try {
      const params = new URLSearchParams();
      if (batchProgress.dataset.batch) params.set('batch', batchProgress.dataset.batch);
      const response = await fetch('/api/progress?' + params, {cache: 'no-store'});
      if (!response.ok) return;
      const {batch, writes} = await response.json();
      current = batch?.status === 'running' || writes > 0;
      const justFinished = busy && !current;
      if (current) dismissed = false;
      if (justFinished) batchProgress.dataset.batch = '';
      if (current || justFinished) {
        const importing = batch?.status === 'running';
        title.textContent = importing ? '文章处理进度' : current ? '语雀写回进度' : '本轮处理已结束';
        bar.value = importing ? batch.pct : 100;
        if (!importing && current) bar.removeAttribute('value');
        meta.textContent = importing ? batch.done + ' / ' + batch.total + ' 已完成' : '';
        const article = batch?.current;
        detail.textContent = importing ? [article?.title, article?.stage_detail].filter(Boolean).join(' · ') || '等待处理' :
          current ? '待写回 ' + writes + ' 篇' : '刷新可查看最新状态';
        dismiss.hidden = current;
        refresh.hidden = current;
        batchProgress.hidden = dismissed;
      }
      busy = current;
    } catch {
      if (busy) detail.textContent = '暂未能读取进度，正在重试';
    } finally {
      window.setTimeout(pollProgress, current ? 1500 : 4500);
    }
  }
  pollProgress();
}

const editProfile = byId('edit-profile');
if (editProfile) {
  const form = byId('profile-form');
  const reading = byId('profile-reading');
  editProfile.addEventListener('click', () => {
    const editing = form.hidden;
    form.hidden = !editing;
    reading.hidden = editing;
    editProfile.setAttribute('aria-expanded', String(editing));
    editProfile.textContent = editing ? '收起编辑' : '编辑档案';
    if (editing) byId('profile-content').focus();
  });
}

if (terms) {
  const checkboxes = [...terms.querySelectorAll('input[name="term_ids"]')];
  const preview = byId('preview-wrap');
  let timer;
  let request;
  function updateTermCount() {
    const selected = checkboxes.filter(input => input.checked).length;
    byId('term-selection-count').textContent = '已选 ' + selected + ' / ' + checkboxes.length;
  }
  function schedulePreview() {
    updateTermCount();
    clearTimeout(timer);
    request?.abort();
    timer = setTimeout(async () => {
      request = new AbortController();
      try {
        const response = await fetch('/docs/' + terms.dataset.docId + '/preview', {
          method: 'POST', body: new FormData(terms), signal: request.signal,
        });
        if (response.ok) preview.innerHTML = await response.text();
      } catch { /* 下一次编辑重新请求预览。 */ }
    }, 300);
  }
  terms.addEventListener('input', schedulePreview);
  window.addEventListener('pageshow', updateTermCount);
  updateTermCount();
  for (const button of terms.querySelectorAll('[data-select-terms]')) {
    button.addEventListener('click', () => {
      const selected = button.dataset.selectTerms === 'yes';
      for (const input of checkboxes) input.checked = selected;
      schedulePreview();
    });
  }
}

if (review) {
  const bars = [...review.querySelectorAll('.review-scrollbar')];
  const panes = [...review.querySelectorAll('.review-pane-scroll')].map((scroll, index) => ({
    scroll,
    column: scroll.parentElement,
    content: scroll.querySelector('.review-pane-content'),
    bar: bars[index],
    position: 0,
  }));
  const scrollRail = review.querySelector('.review-scroll-rail');
  const resizeRail = review.querySelector('.review-resize-rail');
  const handle = review.querySelector('.review-resize-handle');
  const clamp = (value, min, max) => Math.max(min, Math.min(max, value));
  const height = () => scrollRail.offsetHeight;
  const availableWidth = () => review.getBoundingClientRect().width - scrollRail.getBoundingClientRect().width;
  let readerShare;
  const currentShare = () => readerShare ?? panes[0].column.getBoundingClientRect().width / availableWidth();

  function limits() {
    const style = getComputedStyle(review);
    let top = 0;
    for (let box = review; box; box = box.offsetParent) top += box.offsetTop;
    const remaining = document.documentElement.clientHeight - top;
    // Preserve the minimum usable space when a long title or notice pushes the rail down.
    const space = Math.min(review.clientHeight, Math.max(parseFloat(style.minHeight), remaining));
    const max = Math.max(0, Math.floor(space - resizeRail.offsetHeight - parseFloat(style.paddingBottom)));
    return {min: clamp(max - 24, 0, 280), max};
  }
  function syncBar(pane) {
    if (pane.column.hidden) return;
    const {scroll, bar} = pane;
    const overflow = scroll.scrollHeight - scroll.clientHeight;
    pane.position = scroll.scrollTop;
    bar.disabled = overflow <= 1;
    bar.value = overflow > 1 ? 100 * pane.position / overflow : 0;
  }
  function updateLayout(requestedHeight) {
    if (!splitReview.matches) return;
    const {min, max} = limits();
    if (requestedHeight !== undefined) {
      review.style.setProperty('--review-window-height', clamp(Math.round(requestedHeight), min, max) + 'px');
    }
    review.style.setProperty('--review-fit-limit', max + 'px');
    const currentHeight = height();
    const columns = readerShare === 0 ? '仅显示注释' : readerShare === 1 ? '仅显示正文' :
      '正文占双栏的 ' + Math.round(currentShare() * 100) + '%';
    handle.setAttribute('aria-valuemin', min);
    handle.setAttribute('aria-valuemax', max);
    handle.setAttribute('aria-valuenow', currentHeight);
    handle.setAttribute('aria-valuetext', '高度 ' + currentHeight + ' 像素，' + columns);
    panes.forEach(syncBar);
  }
  function syncVisibility() {
    panes.forEach((pane, index) => {
      const hidden = splitReview.matches && readerShare === index;
      if (pane.column.hidden === hidden) return;
      pane.column.hidden = pane.bar.hidden = hidden;
      if (!hidden) pane.scroll.scrollTop = pane.position;
    });
  }
  function setReaderShare(value) {
    readerShare = clamp(value, 0, 1);
    review.style.setProperty('--review-reader-share', readerShare + 'fr');
    review.style.setProperty('--review-notes-share', (1 - readerShare) + 'fr');
    syncVisibility();
  }

  let frame = 0;
  function queueLayout() {
    if (frame) return;
    frame = requestAnimationFrame(() => {
      frame = 0;
      updateLayout();
    });
  }
  const observer = new ResizeObserver(queueLayout);
  observer.observe(review);
  window.addEventListener('resize', queueLayout);
  splitReview.addEventListener('change', () => {
    syncVisibility();
    queueLayout();
  });
  for (const pane of panes) {
    const {scroll, content, bar} = pane;
    scroll.addEventListener('scroll', () => syncBar(pane), {passive: true});
    bar.addEventListener('input', () => {
      scroll.scrollTop = (scroll.scrollHeight - scroll.clientHeight) * bar.valueAsNumber / 100;
    });
    observer.observe(scroll);
    observer.observe(content);
  }

  let drag;
  handle.addEventListener('pointerdown', event => {
    if (event.button !== 0 || drag) return;
    drag = {id: event.pointerId, x: event.clientX, y: event.clientY, height: height(),
      share: currentShare(), width: availableWidth(), moved: false, movedWidth: false};
    handle.focus({preventScroll: true});
    handle.setPointerCapture(event.pointerId);
    review.classList.add('resizing');
    event.preventDefault();
  });
  function followDrag(event) {
    if (drag?.id !== event.pointerId || !splitReview.matches) return;
    const dx = event.clientX - drag.x;
    const dy = event.clientY - drag.y;
    if (!dx && !dy && !drag.moved) return;
    drag.moved = true;
    if (dx || drag.movedWidth) {
      drag.movedWidth = true;
      setReaderShare(drag.share + dx / drag.width);
    }
    updateLayout(drag.height + dy);
  }
  handle.addEventListener('pointermove', followDrag);
  handle.addEventListener('pointerup', followDrag);
  handle.addEventListener('lostpointercapture', () => {
    drag = null;
    review.classList.remove('resizing');
  });
  handle.addEventListener('keydown', event => {
    if (!splitReview.matches) return;
    if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
      event.preventDefault();
      const currentHeight = height();
      const step = event.key === 'ArrowLeft' ? -.05 : .05;
      setReaderShare(currentShare() + step);
      updateLayout(currentHeight);
      return;
    }
    const {min, max} = limits();
    const choices = {ArrowUp: height() - 32, ArrowDown: height() + 32, Home: min, End: max};
    if (!Object.hasOwn(choices, event.key)) return;
    event.preventDefault();
    updateLayout(choices[event.key]);
  });

  // Restore the review layout and scroll positions when a form returns to the same article.
  const reviewKey = 'review-position:' + review.dataset.articleId;
  let saved;
  try {
    saved = JSON.parse(sessionStorage.getItem(reviewKey));
    sessionStorage.removeItem(reviewKey);
  } catch { /* 浏览器可能禁止会话存储。 */ }
  if (splitReview.matches && Number.isFinite(saved?.readerShare)) setReaderShare(saved.readerShare);
  updateLayout(Number.isFinite(saved?.height) ? saved.height : undefined);
  if (splitReview.matches && Array.isArray(saved?.tops)) {
    panes.forEach((pane, index) => {
      if (!Number.isFinite(saved.tops[index])) return;
      pane.position = saved.tops[index];
      if (!pane.column.hidden) pane.scroll.scrollTop = pane.position;
    });
  }
  review.addEventListener('submit', event => {
    if (!splitReview.matches || (event.submitter?.formAction || event.target.action).endsWith('/write')) return;
    const tops = panes.map(pane => pane.column.hidden ? pane.position : pane.scroll.scrollTop);
    try {
      sessionStorage.setItem(reviewKey, JSON.stringify({height: height(), readerShare, tops}));
    } catch { /* 浏览器可能禁止会话存储。 */ }
  });
}

if (!review) {
  const updateHeader = () => document.documentElement.classList.toggle('scrolled', window.scrollY > 26);
  window.addEventListener('scroll', updateHeader, {passive: true});
  updateHeader();
}
