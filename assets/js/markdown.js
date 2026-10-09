'use strict';

(() => {
  const escapeHtml = (value) =>
    String(value ?? '').replace(
      /[&<>'"]/g,
      (character) =>
        ({
          '&': '&amp;',
          '<': '&lt;',
          '>': '&gt;',
          "'": '&#39;',
          '"': '&quot;',
        })[character],
    );

  const safeHref = (value) => {
    const href = String(value ?? '').trim();
    if (!href || /[\u0000-\u001f\u007f]/.test(href)) return '';
    if (/^(https?:|mailto:|tel:)/i.test(href)) return href;
    if (/^(?:\/\/|\/|\.\/|\.\.\/|#|\?)/.test(href)) return href;
    return '';
  };

  const renderInline = (source, depth = 0) => {
    const value = String(source ?? '');
    if (depth > 8) return escapeHtml(value);

    const token = /`([^`\n]+)`|\[([^\]\n]+)\]\(([^)\n]+)\)|\*\*([^*\n]+)\*\*|__([^_\n]+)__|\*([^*\n]+)\*|_([^_\n]+)_/g;
    let output = '';
    let cursor = 0;
    let match;

    while ((match = token.exec(value))) {
      output += escapeHtml(value.slice(cursor, match.index));
      if (match[1] !== undefined) {
        output += `<code>${escapeHtml(match[1])}</code>`;
      } else if (match[2] !== undefined) {
        const href = safeHref(match[3]);
        if (!href) {
          output += escapeHtml(match[0]);
        } else {
          const external = /^(?:https?:)?\/\//i.test(href);
          output += `<a href="${escapeHtml(href)}"${external ? ' target="_blank" rel="noopener noreferrer"' : ''}>${renderInline(match[2], depth + 1)}</a>`;
        }
      } else if (match[4] !== undefined || match[5] !== undefined) {
        output += `<strong>${renderInline(match[4] ?? match[5], depth + 1)}</strong>`;
      } else {
        output += `<em>${renderInline(match[6] ?? match[7], depth + 1)}</em>`;
      }
      cursor = token.lastIndex;
    }

    return output + escapeHtml(value.slice(cursor));
  };

  const render = (source) => {
    const lines = String(source ?? '').replace(/\r\n?/g, '\n').split('\n');
    const blocks = [];
    let paragraph = [];
    let listType = '';
    let listItems = [];

    const flushParagraph = () => {
      if (!paragraph.length) return;
      blocks.push(`<p>${paragraph.map((line) => renderInline(line)).join('<br>')}</p>`);
      paragraph = [];
    };
    const flushList = () => {
      if (!listItems.length) return;
      blocks.push(`<${listType}>${listItems.map((item) => `<li>${renderInline(item)}</li>`).join('')}</${listType}>`);
      listType = '';
      listItems = [];
    };

    lines.forEach((line) => {
      if (!line.trim()) {
        flushParagraph();
        flushList();
        return;
      }

      const unordered = line.match(/^\s{0,3}[-+*]\s+(.+)$/);
      const ordered = line.match(/^\s{0,3}\d+[.)]\s+(.+)$/);
      if (unordered || ordered) {
        flushParagraph();
        const nextType = unordered ? 'ul' : 'ol';
        if (listType && listType !== nextType) flushList();
        listType = nextType;
        listItems.push((unordered || ordered)[1]);
        return;
      }

      flushList();
      paragraph.push(line);
    });

    flushParagraph();
    flushList();
    return blocks.join('');
  };

  window.PortfolioMarkdown = Object.freeze({ render });
})();
