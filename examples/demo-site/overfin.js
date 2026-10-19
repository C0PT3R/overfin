// Overfin JavaScript SDK.
//
// One ES module, no dependencies, no build step: import it straight from a
// browser. It wraps the API (/api/v1) and carries the behavior every client
// needs: following the login when it lapses, typed errors, telling Overfin
// what the browser can play, and a player that reports progress, recovers
// streams the server freed, and handles takeovers.
//
//   import { Overfin } from './overfin.js';
//   const overfin = new Overfin();
//   const me = await overfin.me();
//
// Types are in overfin.d.ts; the API itself is specified in
// api/openapi.yaml.

/** An error from the API: an RFC 9457 problem. Branch on `type`. */
export class OverfinError extends Error {
  constructor(status, problem) {
    super(problem.detail || problem.title || `HTTP ${status}`);
    this.name = 'OverfinError';
    this.status = status;
    this.type = problem.type || 'internal-error';
    this.problem = problem;
  }
  /** Seconds to wait before trying again, for rate-limited and pin-locked. */
  get retryAfter() { return this.problem.retryAfter; }
}

export class Overfin {
  /**
   * @param {object} [options]
   * @param {string} [options.base] Where the API is; '/api/v1' by default.
   * @param {(loginUrl: string) => void} [options.onLoginRequired] Called when
   *   the login lapsed. By default, the browser goes through the login and
   *   comes back to the current page.
   */
  constructor({ base = '/api/v1', onLoginRequired } = {}) {
    this.base = base;
    this.onLoginRequired = onLoginRequired || ((url) => { location.href = url; });
  }

  // ---- Requests ----------------------------------------------------------

  /** Calls the API. Resolves with the JSON body, or null for no content. */
  async request(method, path, body, { keepalive = false } = {}) {
    const init = { method, headers: {}, keepalive };
    if (body !== undefined) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    const res = await fetch(this.base + path, init);
    if (res.status === 204) return null;
    const data = await res.json().catch(() => ({}));
    if (!res.ok) {
      const err = new OverfinError(res.status, data);
      if (err.type === 'not-authenticated') this.onLoginRequired(this.loginUrl());
      throw err;
    }
    return data;
  }

  /** The login address, coming back to returnTo (the current page by default). */
  loginUrl(returnTo = location.pathname + location.search + location.hash) {
    return `${this.base}/login?return=${encodeURIComponent(returnTo)}`;
  }

  /**
   * Confirms a fresh login before a destructive action: the browser goes to
   * the identity provider and comes back to returnTo. Call it after an
   * OverfinError of type 'step-up-required', then retry the action.
   */
  stepUp(error, returnTo = location.pathname + location.search + location.hash) {
    const url = error?.problem?.stepUpUrl;
    if (!url) throw new Error('this server offers no step-up');
    location.href = `${url}?return=${encodeURIComponent(returnTo)}`;
  }

  // ---- Account -----------------------------------------------------------

  /** The account and active profile. Fails with 'account-setup-required' before the first visit. */
  me() { return this.request('GET', '/me'); }
  /** Creates a fresh account. */
  createAccount() { return this.request('POST', '/me', {}); }
  /** Brings an existing media server account, with its history. The password is used once. */
  claimAccount(username, password) { return this.request('POST', '/me/claim', { username, password }); }
  /** Ends this session (and the login). Resolves with { logoutUrl? }. */
  logout() { return this.request('POST', '/logout', {}); }

  // ---- Profiles ----------------------------------------------------------

  async profiles() { return (await this.request('GET', '/profiles')).items; }
  profile(id) { return this.request('GET', `/profiles/${enc(id)}`); }
  /** @param {{name: string, kind?: 'standard'|'kids', ratingCeiling?: string, language?: string}} profile */
  createProfile(profile) { return this.request('POST', '/profiles', profile); }
  /** Changes name, language, kind or ratingCeiling; limits need the owner's `pin` if set. */
  updateProfile(id, changes) { return this.request('PATCH', `/profiles/${enc(id)}`, changes); }
  /** Deletes a profile and its history. Needs a fresh login (step-up). */
  deleteProfile(id) { return this.request('DELETE', `/profiles/${enc(id)}`); }
  /** Switches this session to a profile, with its PIN if it has one. */
  activateProfile(id, pin) { return this.request('POST', `/profiles/${enc(id)}/activate`, pin ? { pin } : {}); }
  setPin(id, pin, ownerPin) { return this.request('PUT', `/profiles/${enc(id)}/pin`, ownerPin ? { pin, ownerPin } : { pin }); }
  removePin(id) { return this.request('DELETE', `/profiles/${enc(id)}/pin`); }
  /** Rating names a kids profile's ceiling can be, least mature first. */
  async ratings() { return (await this.request('GET', '/ratings')).items.map((r) => r.name); }
  async sessions() { return (await this.request('GET', '/sessions')).items; }
  endSession(id) { return this.request('DELETE', `/sessions/${enc(id)}`); }
  endOtherSessions() { return this.request('DELETE', '/sessions'); }

  // ---- Library -----------------------------------------------------------

  async libraries() { return (await this.request('GET', '/libraries')).items; }
  item(id) { return this.request('GET', `/items/${enc(id)}`); }

  /**
   * One page of items. query: library, kind (string or list), parent, genre,
   * person, studio, yearFrom, yearTo, played, favorite, inProgress, sort
   * (name, addedAt, premiereDate, communityRating, lastPlayed, random),
   * order (asc, desc), seed, limit, cursor. Resolves with
   * { items, nextCursor?, totalCount?, seed? }.
   */
  items(query = {}) { return this.request('GET', '/items' + toQuery(query)); }

  /** Every page of a query, one after the other: for await (const page of overfin.pages(q)). */
  async *pages(query = {}) {
    let cursor = query.cursor;
    let seed = query.seed;
    do {
      const page = await this.items({ ...query, cursor, seed });
      yield page;
      cursor = page.nextCursor;
      seed = seed || page.seed;
    } while (cursor);
  }

  async resume(limit = 12) { return (await this.request('GET', `/items/resume${toQuery({ limit })}`)).items; }
  async nextUp(limit = 12) { return (await this.request('GET', `/items/next-up${toQuery({ limit })}`)).items; }
  async latest(libraryId, limit = 16) { return (await this.request('GET', `/items/latest${toQuery({ library: libraryId, limit })}`)).items; }

  /** The address of an image reference, in a size: 'small', 'medium' or 'large'. */
  imageUrl(ref, size = 'medium') { return ref ? `${ref.url}?size=${size}` : ''; }

  // ---- Playback ----------------------------------------------------------

  /** Starts a playback. Most clients want play() instead, which does the rest. */
  startPlayback(request) { return this.request('POST', '/playback', request); }
  playback(id) { return this.request('GET', `/playback/${enc(id)}`); }
  currentPlayback() { return this.request('GET', '/playback/current'); }
  reportProgress(id, report) { return this.request('POST', `/playback/${enc(id)}/progress`, report); }
  stopPlayback(id, positionMs, { keepalive = false } = {}) {
    return this.request('POST', `/playback/${enc(id)}/stop`, { positionMs }, { keepalive });
  }

  /**
   * Plays an item in a <video> element, and keeps it playing: see Player.
   * @param {HTMLVideoElement} video
   * @param {string} itemId
   * @param {PlayerOptions} [options]
   */
  async play(video, itemId, options = {}) {
    const player = new Player(this, video, itemId, options);
    await player.start();
    return player;
  }
}

// ---- Capabilities ---------------------------------------------------------

/**
 * What this browser can play, for Overfin to pick direct play, repackaging or
 * transcoding. Clients can adjust it before passing it on.
 */
export function capabilities() {
  const video = document.createElement('video');
  const mse = globalThis.ManagedMediaSource || globalThis.MediaSource;
  const can = (mime) => Boolean((mse && mse.isTypeSupported(mime)) || video.canPlayType(mime));
  const pick = (table) => Object.entries(table).filter(([, mime]) => can(mime)).map(([name]) => name);
  const pixels = Math.max(screen.width, screen.height) * (globalThis.devicePixelRatio || 1);
  return {
    containers: pick({ mp4: 'video/mp4; codecs="avc1.640028"', webm: 'video/webm; codecs="vp9"' }),
    videoCodecs: pick({
      h264: 'video/mp4; codecs="avc1.640028"',
      hevc: 'video/mp4; codecs="hvc1.1.6.L120.90"',
      av1: 'video/mp4; codecs="av01.0.08M.08"',
      vp9: 'video/mp4; codecs="vp09.00.10.08"',
    }),
    audioCodecs: pick({
      aac: 'audio/mp4; codecs="mp4a.40.2"',
      mp3: 'audio/mpeg',
      opus: 'audio/mp4; codecs="opus"',
      flac: 'audio/mp4; codecs="flac"',
      ac3: 'audio/mp4; codecs="ac-3"',
      eac3: 'audio/mp4; codecs="ec-3"',
    }),
    maxHeight: pixels >= 3000 ? 2160 : 1080,
    hdr: matchMedia('(dynamic-range: high)').matches ? ['hdr10'] : [],
    subtitles: ['vtt'],
    maxAudioChannels: 2,
  };
}

// ---- Player ---------------------------------------------------------------

/**
 * @typedef {object} PlayerOptions
 * @property {number} [startMs] Where to start; by default where the profile
 *   left off.
 * @property {string} [audioTrack]
 * @property {string} [subtitleTrack]
 * @property {object} [capabilities] What the browser can play; detected by default.
 * @property {(playback: object) => void} [onStart] Called on every (re)start,
 *   with the playback: tracks, segments, method.
 * @property {(device: string) => void} [onTakenOver] Another device of the
 *   account took over; player.continueHere() takes it back.
 * @property {(error: Error) => void} [onError] The playback can't go on.
 * @property {() => void} [onEnded] Played to the end.
 * @property {object} [Hls] The hls.js constructor, for browsers without
 *   native HLS; globalThis.Hls by default.
 */

const PROGRESS_EVERY = 10_000;   // ms between progress reports while playing
const LONG_PAUSE = 90_000;       // after this, check the stream still exists

/**
 * Keeps one item playing in a <video> element: reports progress, restarts a
 * stream the server freed (after a long pause or a lost connection) at the
 * same place without a word, switches audio and subtitles, and reports a
 * takeover by another device.
 */
export class Player {
  constructor(client, video, itemId, options = {}) {
    this.client = client;
    this.video = video;
    this.itemId = itemId;
    this.options = options;
    // What the server is asked for (audio, and a burned-in subtitle track),
    // and which text subtitle track the browser shows.
    this.choice = { audioTrack: options.audioTrack };
    this.textTrack = null;
    this.wanted = options.subtitleTrack || null;
    this.playback = null;
    this.hls = null;
    this.timer = null;
    this.pausedAt = 0;
    this.closed = false;
    this.handlers = {
      pause: () => { this.pausedAt = Date.now(); this.report(); },
      play: () => {
        if (this.pausedAt && Date.now() - this.pausedAt > LONG_PAUSE) this.check();
        this.pausedAt = 0;
      },
      seeked: () => this.report(),
      ended: () => { this.report({ ended: true }); this.close(); this.options.onEnded?.(); },
    };
  }

  /** The position, in ms. */
  get positionMs() { return Math.round(this.video.currentTime * 1000); }

  /** Starts, or restarts at the current position. */
  async start(startMs = this.options.startMs) {
    this.teardown();
    let at = startMs;
    if (at === undefined) {
      const item = await this.client.item(this.itemId);
      at = item.userState?.positionMs || 0;
    }
    let pb;
    try {
      pb = await this.client.startPlayback({
        itemId: this.itemId,
        capabilities: this.options.capabilities || capabilities(),
        startPositionMs: Math.round(at),
        ...clean(this.choice),
      });
    } catch (err) {
      this.options.onError?.(err);
      throw err;
    }
    this.playback = pb;
    this.attach(pb);
    for (const [event, fn] of Object.entries(this.handlers)) this.video.addEventListener(event, fn);
    this.timer = setInterval(() => { if (!this.video.paused) this.report(); }, PROGRESS_EVERY);
    this.options.onStart?.(pb);
    return pb;
  }

  // attach points the video element at the stream, with its subtitles.
  attach(pb) {
    const video = this.video;
    for (const t of video.querySelectorAll('track[data-overfin]')) t.remove();
    for (const t of pb.subtitleTracks || []) {
      if (!t.url) continue;
      const el = document.createElement('track');
      Object.assign(el, { kind: 'subtitles', src: t.url, srclang: t.language || 'und', label: t.label });
      el.dataset.overfin = t.id;
      video.append(el);
    }
    const startAt = (pb.positionMs || 0) / 1000;
    const Hls = this.options.Hls || globalThis.Hls;
    if (pb.stream.protocol === 'hls' && Hls && Hls.isSupported()) {
      this.hls = new Hls({ startPosition: startAt });
      this.hls.on(Hls.Events.ERROR, (_, data) => { if (data.fatal) this.check(); });
      this.hls.loadSource(pb.stream.url);
      this.hls.attachMedia(video);
    } else if (pb.stream.protocol === 'hls' && !video.canPlayType('application/vnd.apple.mpegurl')) {
      throw new Error("this browser can't play HLS; load hls.js");
    } else {
      video.src = pb.stream.url;
      if (startAt) video.addEventListener('loadedmetadata', () => { video.currentTime = startAt; }, { once: true });
    }
    // A subtitle asked for in the options: shown if it's text, burned in
    // (one restart) if it's an image.
    if (this.wanted) {
      const id = this.wanted;
      this.wanted = null;
      queueMicrotask(() => this.setSubtitleTrack(id));
    }
    this.showSubtitles(this.textTrack);
  }

  /** Switches the audio track: the stream restarts at the same place. */
  setAudioTrack(id) {
    this.choice.audioTrack = id;
    return this.restart();
  }

  /**
   * Shows a subtitle track, or none (null). Text tracks switch at once;
   * image-based ones are burned in, which restarts the stream.
   */
  setSubtitleTrack(id) {
    const tracks = this.playback?.subtitleTracks || [];
    const isText = (tid) => Boolean(tracks.find((t) => t.id === tid)?.url);
    const burnedIn = Boolean(this.choice.subtitleTrack);
    if (id && !isText(id)) {
      // An image track: the server burns it into the picture.
      this.choice.subtitleTrack = id;
      this.textTrack = null;
      return this.restart();
    }
    this.textTrack = id || null;
    if (burnedIn) {
      // Leaving a burned-in track needs a clean picture.
      this.choice.subtitleTrack = undefined;
      return this.restart();
    }
    this.showSubtitles(this.textTrack);
    return Promise.resolve();
  }

  /** The subtitle track on screen, or null. */
  get subtitleTrack() { return this.choice.subtitleTrack || this.textTrack; }

  /** The audio track playing. */
  get audioTrack() { return this.playback?.audioTrack; }

  showSubtitles(id) {
    for (const el of this.video.querySelectorAll('track[data-overfin]')) {
      el.track.mode = el.dataset.overfin === id ? 'showing' : 'disabled';
    }
  }

  /** The segment (intro, credits…) at a time, if any. */
  segmentAt(ms = this.positionMs) {
    return (this.playback?.segments || []).find((s) => ms >= s.startMs && ms < s.endMs) || null;
  }

  /** Takes playback back after another device took over. */
  continueHere() {
    this.video.autoplay = true;
    return this.restart();
  }

  /** Stops for good: the server frees the stream at once. */
  async close() {
    if (this.closed) return;
    const pb = this.playback;
    const position = this.positionMs;
    this.teardown();
    this.closed = true;
    this.video.removeAttribute('src');
    this.video.load();
    if (pb) await this.client.stopPlayback(pb.id, position, { keepalive: true }).catch(() => {});
  }

  // ---- Inside --------------------------------------------------------------

  restart() {
    const wasPlaying = !this.video.paused;
    const at = this.positionMs;
    const old = this.playback;
    this.teardown();
    if (old) this.client.stopPlayback(old.id, at).catch(() => {});
    this.video.autoplay = this.video.autoplay || wasPlaying;
    return this.start(at);
  }

  teardown() {
    clearInterval(this.timer);
    this.timer = null;
    for (const [event, fn] of Object.entries(this.handlers)) this.video.removeEventListener(event, fn);
    if (this.hls) { this.hls.destroy(); this.hls = null; }
  }

  report(extra = {}) {
    const pb = this.playback;
    if (!pb || this.closed) return;
    this.client.reportProgress(pb.id, { positionMs: this.positionMs, paused: this.video.paused, ...extra })
      .catch((err) => this.gone(pb, err));
  }

  async check() {
    const pb = this.playback;
    if (!pb) return;
    try { await this.client.playback(pb.id); } catch (err) { this.gone(pb, err); }
  }

  // gone handles a playback the server no longer has.
  gone(pb, err) {
    if (pb !== this.playback || this.closed || err.status !== 410) return;
    if (err.type === 'playback-expired') {
      // Freed after a pause or a lost connection: carry on, unnoticed.
      this.video.autoplay = !this.video.paused;
      this.restart();
      return;
    }
    this.teardown();
    this.playback = null;
    this.video.pause();
    if (err.type === 'playback-taken-over' && this.options.onTakenOver) {
      this.playback = pb; // continueHere() restarts from it
      this.options.onTakenOver(err.problem.takenOverBy || '');
    } else {
      this.options.onError?.(err);
    }
  }
}

// ---- Helpers ---------------------------------------------------------------

const enc = encodeURIComponent;

function toQuery(query) {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined || v === null || v === '') continue;
    for (const one of Array.isArray(v) ? v : [v]) q.append(k, String(one));
  }
  const s = q.toString();
  return s ? '?' + s : '';
}

function clean(obj) {
  return Object.fromEntries(Object.entries(obj).filter(([, v]) => v !== undefined && v !== null));
}
