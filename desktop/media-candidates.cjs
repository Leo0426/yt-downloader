'use strict';

function httpURL(value) {
  if (typeof value !== 'string' || value.length > 16384) return null;
  let url;
  try { url = new URL(value); } catch { return null; }
  if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password) return null;
  return url;
}

function responseHeader(headers, key) {
  const values = Object.entries(headers || {}).find(([name]) => name.toLowerCase() === key)?.[1];
  return Array.isArray(values) ? values[0] || '' : values || '';
}

function identifyResponse(details) {
  const {method, statusCode, resourceType} = details;
  if (method !== 'GET' || (statusCode !== 200 && statusCode !== 206)) return null;
  if (['image', 'font', 'stylesheet', 'script', 'webSocket', 'ping'].includes(resourceType)) return null;

  const url = httpURL(details.url);
  if (!url) return null;
  url.hash = '';
  const type = responseHeader(details.responseHeaders, 'content-type').split(';')[0].trim().toLowerCase();
  const file = url.pathname.toLowerCase();
  // Segments and initialization files cannot substitute for the parent manifest.
  if (/\.(ts|m4s|cmfv|cmfa|cmf[av]i|aac)$/i.test(file) || type === 'video/mp2t') return null;
  if (type === 'text/html' || type === 'application/xhtml+xml' || type === 'application/json' || type.startsWith('image/')) return null;

  let kind;
  if (/\.m3u8$/.test(file) || ['application/vnd.apple.mpegurl','application/x-mpegurl','audio/mpegurl','audio/x-mpegurl'].includes(type)) kind = 'HLS';
  else if (/\.mpd$/.test(file) || type === 'application/dash+xml') kind = 'DASH';
  // Some sites fetch individual ranges as different URLs. Those are incomplete.
  else if (type.startsWith('text/')) return null;
  else if (url.searchParams.has('range') && /^\d+-\d*$/.test(url.searchParams.get('range'))) return null;
  else if (/\.(mp3|m4a|mka|aiff?|opus|oga|ogg|wav|flac|wma)$/.test(file) || type.startsWith('audio/')) kind = '音频';
  else if (/\.mp4$/.test(file) || type === 'video/mp4') kind = 'MP4';
  else if (/\.webm$/.test(file) || type === 'video/webm') kind = 'WebM';
  else if (/\.(mov|mkv|avi|m4v|flv|f4v|ogv|ogm|mpeg|mpg|wmv|asf|3gp|3g2|m2ts|mts|vob)$/.test(file) || type.startsWith('video/')) kind = '视频';
  else return null;

  const total = /\/(\d+)$/.exec(responseHeader(details.responseHeaders, 'content-range'))?.[1];
  const length = statusCode === 200 ? responseHeader(details.responseHeaders, 'content-length') : total;
  return {url: url.href, kind, bytes: /^\d+$/.test(length || '') ? Number(length) : 0};
}

module.exports = {httpURL, identifyResponse};
