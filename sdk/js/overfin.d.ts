// Types for overfin.js. The API is specified in api/openapi.yaml; these
// cover what the SDK returns. Fields marked optional may be absent.

export interface Problem {
  type: string;
  title: string;
  status: number;
  detail?: string;
  retryAfter?: number;
  takenOverBy?: string;
  stepUp?: string;
  stepUpUrl?: string;
  /** For account-setup-required: whether an existing account can be brought. */
  claim?: 'move' | 'link';
}

export class OverfinError extends Error {
  readonly status: number;
  /** Stable error name, such as 'pin-incorrect' or 'step-up-required'. */
  readonly type: string;
  readonly problem: Problem;
  readonly retryAfter?: number;
}

export interface Profile {
  id: string;
  name: string;
  kind: 'standard' | 'kids' | string;
  owner: boolean;
  hasPin: boolean;
  ratingCeiling?: string;
}

export interface Me {
  accountId: string;
  /** Absent until a profile is chosen (when the owner profile has a PIN). */
  activeProfile?: Profile;
}

export interface Session {
  id: string;
  current: boolean;
  device?: string;
  profileName?: string;
  createdAt: string;
  lastActiveAt?: string;
}

export interface ImageRef {
  url: string;
  aspectRatio?: number;
  blurHash?: string;
}

/** A title known from TMDB, in the library or not. */
export interface CatalogTitle {
  tmdbId: string;
  mediaKind: 'movie' | 'series' | (string & {});
  name: string;
  year?: number;
  overview?: string;
  images?: Images;
  /** How much of it the library has: 'available', 'partiallyAvailable', 'missing'… */
  availability: string;
  /** The library item, when the library has it: play it like any other. */
  libraryItem?: ItemSummary;
}

export interface CatalogDetail extends CatalogTitle {
  genres?: string[];
  cast?: { name: string; character?: string }[];
  seasons?: { number: number; name?: string; episodeCount: number; availability: string }[];
}

export interface CatalogPage {
  items: CatalogTitle[];
  nextCursor?: string;
}

export interface Images {
  primary?: ImageRef;
  backdrops?: ImageRef[];
  logo?: ImageRef;
  thumb?: ImageRef;
}

export interface UserState {
  positionMs?: number;
  played: boolean;
  playCount?: number;
  favorite: boolean;
  lastPlayedAt?: string;
  unplayedCount?: number;
}

export interface ItemSummary {
  id: string;
  kind: string;
  name: string;
  premiereDate?: string;
  year?: number;
  durationMs?: number;
  officialRating?: string;
  images?: Images;
  userState?: UserState;
  movie?: { originalTitle?: string; tagline?: string };
  series?: { status?: string; seasonCount?: number; endDate?: string };
  season?: { seriesId: string; seriesName?: string; number?: number; episodeCount?: number };
  episode?: { seriesId: string; seriesName?: string; seasonId?: string; seasonNumber?: number; number?: number; numberEnd?: number };
  [kindPart: string]: unknown;
}

export interface Person {
  id: string;
  name: string;
  overview?: string;
  image?: ImageRef;
  birthDate?: string;
  deathDate?: string;
  externalIds?: Record<string, string>;
}

export interface Credit {
  person: { id: string; name: string; image?: ImageRef };
  role: string;
  character?: string;
}

export interface Item extends ItemSummary {
  overview?: string;
  genres?: string[];
  studios?: string[];
  communityRating?: number;
  criticRating?: number;
  credits?: Credit[];
  externalIds?: Record<string, string>;
}

export interface Library {
  id: string;
  name: string;
  kind: string;
  image?: ImageRef;
}

export interface ItemQuery {
  library?: string;
  kind?: string | string[];
  parent?: string;
  genre?: string;
  person?: string;
  studio?: string;
  yearFrom?: number;
  yearTo?: number;
  played?: boolean;
  favorite?: boolean;
  inProgress?: boolean;
  sort?: 'name' | 'addedAt' | 'premiereDate' | 'communityRating' | 'lastPlayed' | 'random';
  order?: 'asc' | 'desc';
  seed?: string;
  limit?: number;
  cursor?: string;
}

export interface ItemPage {
  items: ItemSummary[];
  nextCursor?: string;
  totalCount?: number;
  seed?: string;
}

export interface Capabilities {
  containers?: string[];
  videoCodecs?: string[];
  audioCodecs?: string[];
  maxHeight?: number;
  hdr?: string[];
  subtitles?: string[];
  maxAudioChannels?: number;
  maxBitrateKbps?: number;
}

export interface Track {
  id: string;
  label: string;
  language?: string;
  codec?: string;
  channels?: number;
  default?: boolean;
  forced?: boolean;
  hearingImpaired?: boolean;
  /** Text subtitles only: WebVTT the browser shows itself. */
  url?: string;
}

export interface Segment {
  kind: 'intro' | 'outro' | 'recap' | 'preview' | 'commercial' | string;
  startMs: number;
  endMs: number;
}

export interface Playback {
  id: string;
  item: ItemSummary;
  method: 'direct' | 'remux' | 'transcode' | string;
  stream: { url: string; protocol: 'file' | 'hls'; mimeType?: string };
  positionMs: number;
  durationMs?: number;
  versions?: { id: string; name: string }[];
  versionId?: string;
  audioTracks?: Track[];
  audioTrack?: string;
  subtitleTracks?: Track[];
  subtitleTrack?: string;
  maxBitrateKbps?: number;
  segments?: Segment[];
}

export interface PlaybackRequest {
  itemId: string;
  capabilities: Capabilities;
  startPositionMs?: number;
  versionId?: string;
  audioTrack?: string;
  subtitleTrack?: string;
  maxBitrateKbps?: number;
  device?: string;
}

export interface PlayerOptions {
  /** Where to start; by default where the profile left off. */
  startMs?: number;
  audioTrack?: string;
  subtitleTrack?: string;
  /** What the browser can play; detected by default. */
  capabilities?: Capabilities;
  /** Every (re)start, with the playback: tracks, segments, method. */
  onStart?: (playback: Playback) => void;
  /** Another device took over; player.continueHere() takes it back. */
  onTakenOver?: (device: string) => void;
  /** The playback can't go on. */
  onError?: (error: Error) => void;
  /** Played to the end. */
  onEnded?: () => void;
  /** The hls.js constructor; globalThis.Hls by default. */
  Hls?: unknown;
}

export class Player {
  readonly video: HTMLVideoElement;
  readonly itemId: string;
  readonly playback: Playback | null;
  readonly positionMs: number;
  readonly audioTrack?: string;
  readonly subtitleTrack: string | null;
  setAudioTrack(id: string): Promise<unknown>;
  setSubtitleTrack(id: string | null): Promise<unknown>;
  segmentAt(ms?: number): Segment | null;
  continueHere(): Promise<unknown>;
  close(): Promise<void>;
}

export function capabilities(): Capabilities;

export class Overfin {
  constructor(options?: { base?: string; onLoginRequired?: (loginUrl: string) => void });
  readonly base: string;

  request<T = unknown>(method: string, path: string, body?: unknown, options?: { keepalive?: boolean }): Promise<T>;
  loginUrl(returnTo?: string): string;
  stepUp(error: OverfinError, returnTo?: string): void;

  me(): Promise<Me>;
  createAccount(): Promise<Me>;
  claimAccount(username: string, password: string): Promise<Me>;
  logout(): Promise<{ logoutUrl?: string }>;
  deleteAccount(): Promise<{ history: 'deleted' | 'kept' }>;
  exportData(pin?: string): Promise<Record<string, unknown>>;

  profiles(): Promise<Profile[]>;
  profile(id: string): Promise<Profile>;
  createProfile(profile: { name: string; kind?: 'standard' | 'kids'; ratingCeiling?: string; language?: string }): Promise<Profile>;
  updateProfile(id: string, changes: { name?: string; language?: string; kind?: 'standard' | 'kids'; ratingCeiling?: string; pin?: string }): Promise<Profile>;
  deleteProfile(id: string): Promise<null>;
  activateProfile(id: string, pin?: string): Promise<Profile>;
  setPin(id: string, pin: string, ownerPin?: string): Promise<null>;
  removePin(id: string): Promise<null>;
  ratings(): Promise<string[]>;
  sessions(): Promise<Session[]>;
  endSession(id: string): Promise<null>;
  endOtherSessions(): Promise<null>;

  libraries(): Promise<Library[]>;
  item(id: string): Promise<Item>;
  person(id: string): Promise<Person>;
  items(query?: ItemQuery): Promise<ItemPage>;
  pages(query?: ItemQuery): AsyncGenerator<ItemPage>;
  resume(limit?: number): Promise<ItemSummary[]>;
  nextUp(limit?: number): Promise<ItemSummary[]>;
  latest(libraryId: string, limit?: number): Promise<ItemSummary[]>;
  imageUrl(ref: ImageRef | undefined, size?: 'small' | 'medium' | 'large'): string;

  discover(list: 'trending' | 'popular' | 'upcoming', mediaKind: 'movie' | 'series',
    options?: { availability?: 'all' | 'library' | 'missing'; limit?: number; cursor?: string }): Promise<CatalogPage>;
  catalogTitle(mediaKind: 'movie' | 'series', tmdbId: string): Promise<CatalogDetail>;

  watchlist(): Promise<ItemSummary[]>;
  onWatchlist(id: string): Promise<boolean>;
  addToWatchlist(id: string): Promise<null>;
  removeFromWatchlist(id: string): Promise<null>;

  startPlayback(request: PlaybackRequest): Promise<Playback>;
  playback(id: string): Promise<Playback>;
  currentPlayback(): Promise<Playback>;
  reportProgress(id: string, report: { positionMs: number; paused?: boolean; ended?: boolean }): Promise<null>;
  stopPlayback(id: string, positionMs: number, options?: { keepalive?: boolean }): Promise<null>;
  play(video: HTMLVideoElement, itemId: string, options?: PlayerOptions): Promise<Player>;
}
