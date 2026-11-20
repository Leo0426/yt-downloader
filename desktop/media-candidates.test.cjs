'use strict';

const assert = require('node:assert/strict');
const {test} = require('node:test');
const {identifyResponse, httpURL} = require('./media-candidates.cjs');

function response(url, type, options = {}) {
  return identifyResponse({
    method: 'GET', statusCode: 200, resourceType: 'xhr', url,
    responseHeaders: {'Content-Type': [type]},
    ...options,
  });
}

test('keeps the full signed manifest URL and recognizes plain-text playlists', () => {
  const url = 'https://cdn.example/stream.m3u8?Expires=7&Signature=abc%2Fxyz';
  assert.deepEqual(response(url + '#start', 'text/plain'), {url, kind: 'HLS', bytes: 0});
  assert.equal(response('https://cdn.example/manifest', 'application/dash+xml').kind, 'DASH');
});

test('reports the full file size for a video range response', () => {
  const url = 'https://cdn.example/playback';
  const result = response(url, 'video/mp4', {
    statusCode: 206, resourceType: 'media',
    responseHeaders: {'CONTENT-TYPE': ['video/mp4'], 'content-range': ['bytes 0-99/2048']},
  });
  assert.deepEqual(result, {url, kind: 'MP4', bytes: 2048});
});

test('skips resource fragments and unsuccessful or non-media responses', () => {
  assert.equal(response('https://cdn.example/chunk.m4s', 'video/mp4'), null);
  assert.equal(response('https://cdn.example/video.mp4?range=0-512', 'video/mp4'), null);
  assert.equal(response('https://cdn.example/index.m3u8', 'text/html'), null);
  assert.equal(response('https://cdn.example/video.mp4', 'video/mp4', {statusCode: 403}), null);
  assert.equal(response('https://cdn.example/metrics', 'application/json'), null);
  assert.equal(httpURL('file:///tmp/clip.mp4'), null);
  assert.equal(httpURL('https://joe:password@cdn.example/video.mp4'), null);
});

test('recognizes legacy video and lossless audio by filename with a generic content type', () => {
  assert.equal(response('https://media.example/1997.3gp', 'application/octet-stream').kind, '视频');
  assert.equal(response('https://media.example/footage.wmv', 'application/octet-stream').kind, '视频');
  assert.equal(response('https://media.example/track.flac', 'application/octet-stream').kind, '音频');
});
