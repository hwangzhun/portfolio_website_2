'use strict';

(async () => {
  const base = '';
  const id = new URLSearchParams(location.search).get('id');
  let playbackRequestId = '';
  const mediaUrl = (url) => url && (url.startsWith('/uploads/') || url.startsWith('/assets/')) ? base + url : url;
  const cleanUrl = (value) => {
    try {
      const parsed = new URL(value, location.origin);
      parsed.search = '';
      parsed.hash = '';
      return parsed.href;
    } catch (_) {
      return String(value || '').slice(0, 300);
    }
  };
  const report = (stage, code, message, resourceUrl = '') => {
    fetch(base + '/api/client-events', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        type: 'vod_error',
        projectId: id || '',
        stage: String(stage || '').slice(0, 80),
        code: String(code || '').slice(0, 80),
        message: String(message || '').slice(0, 400),
        resourceUrl: cleanUrl(resourceUrl),
        pageUrl: cleanUrl(location.href),
        requestId: playbackRequestId,
      }),
      keepalive: true,
    }).catch(() => {});
  };
  const loading = document.querySelector('#videoLoading');
  const projectElement = document.querySelector('#videoProject');
  const missing = document.querySelector('#videoMissing');
  const playerError = document.querySelector('#videoError');
  const showMissing = () => {
    loading.hidden = true;
    projectElement.hidden = true;
    missing.hidden = false;
  };
  const showPlayerError = () => {
    playerError.hidden = false;
  };
  const hidePlayerError = () => {
    playerError.hidden = true;
  };

  try {
    if (!id) {
      report('page-init', 'missing-project-id', '页面地址缺少作品 ID');
      return showMissing();
    }
    const [siteResponse, playbackResponse] = await Promise.all([
      fetch(base + '/api/site'),
      fetch(base + '/api/videos/' + encodeURIComponent(id) + '/playback', { cache: 'no-store' }),
    ]);
    playbackRequestId = playbackResponse.headers.get('X-Request-ID') || '';
    if (siteResponse.status === 404 || playbackResponse.status === 404) {
      report('playback-config', String(playbackResponse.status), '作品未发布、FileID 缺失或作品 ID 不匹配');
      return showMissing();
    }
    if (!siteResponse.ok || !playbackResponse.ok) {
      report('playback-config', `${siteResponse.status}/${playbackResponse.status}`, '站点内容或播放配置接口请求失败');
      throw new Error(`site ${siteResponse.status}, playback ${playbackResponse.status}`);
    }
    const data = await siteResponse.json();
    const playback = await playbackResponse.json();
    const project = (data.projects || []).find((item) => item.id === id && item.type === 'video' && item.published !== false);
    if (!project || !project.vodFileId) {
      report('content-match', 'missing-vod-file-id', '前台内容中未找到视频作品或 VOD FileID');
      return showMissing();
    }
    document.title = `${project.title} — ${data.profile?.siteName || 'Your Portfolio'}`;
    document.querySelectorAll('.wordmark').forEach((mark) => mark.firstChild.textContent = data.profile?.siteName || 'YOUR PORTFOLIO');
    document.querySelectorAll('a[href^="mailto:"]').forEach((link) => link.href = `mailto:${data.profile?.email || ''}`);
    document.querySelector('#videoKicker').textContent = [project.category, project.year].filter(Boolean).join(' · ');
    document.querySelector('#videoTitle').textContent = project.title || '';
    document.querySelector('#videoYear').textContent = project.year || '—';
    document.querySelector('#videoMeta').textContent = project.meta || '—';
    document.querySelector('#videoDescription').textContent = project.description || '这支影片的项目介绍正在整理中。';
    if (typeof window.TCPlayer !== 'function') {
      report('sdk-load', 'tcplayer-unavailable', 'TCPlayer SDK 未加载，请检查 tcsdk.com 是否可访问');
      throw new Error('TCPlayer SDK unavailable');
    }
    const options = { appID: playback.appId, fileID: playback.fileId, psign: playback.psign, licenseUrl: playback.licenseUrl, language: 'zh-CN' };
    if (project.coverUrl) options.poster = mediaUrl(project.coverUrl);
    const player = window.TCPlayer('videoPlayer', options);
    ['loadedmetadata', 'canplay', 'playing'].forEach((eventName) => player.on(eventName, hidePlayerError));
    player.on('error', (event) => {
      const error = event?.data || event || {};
      report('tcplayer', error.code || event?.code || 'player-error', error.message || event?.message || '腾讯云播放器报错');
      showPlayerError();
    });
    loading.hidden = true;
    missing.hidden = true;
    projectElement.hidden = false;
  } catch (error) {
    console.info('无法载入视频项目', error);
    report('page-init', error?.name || 'error', error?.message || String(error));
    loading.hidden = true;
    missing.hidden = true;
    projectElement.hidden = false;
    showPlayerError();
  }
})();
