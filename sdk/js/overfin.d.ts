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
  /** A kids profile's highest allowed certifications, for films and series. */
  movieCeiling?: string;
  seriesCeiling?: string;
  /** A kids profile without ceilings yet: it sees nothing until its owner picks them. */
  ceilingsNeeded?: boolean;
  /** The only libraries the profile gets (their IDs); absent, every library. */
  libraries?: string[];
  /** Tags hidden from the profile. */
  blockedTags?: string[];
}

export interface RatingLevel {
  name: string;
  meaning?: string;
}

/** The certifications a kids profile's ceilings can be, least mature first; empty while the server has no ratings. */
export interface Ratings {
  country: string;
  movie: RatingLevel[];
  series: RatingLevel[];
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

export interface Genre {
  name: string;
  /** How many of the library's titles the profile may see have it. */
  itemCount?: number;
}

export interface Library {
  id: string;
  name: string;
  kind: string;
  image?: ImageRef;
}

/** What a library's titles are filtered and sorted by. */
export interface TitleQuery {
  library?: string;
  kind?: 'movie' | 'series' | ('movie' | 'series')[];
  genre?: string;
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

/** A title, in a library or not: a film, a series, a season, an episode. */
export interface TitleSummary {
  id: string;
  kind: 'movie' | 'series' | 'season' | 'episode' | 'other' | (string & {});
  name: string;
  year?: number;
  /** A season's or an episode's, YYYY-MM-DD. */
  airDate?: string;
  durationMs?: number;
  /** Its rating in the server's ratings country (its series', for a season or an episode). */
  certification?: string;
  availability: 'available' | 'partiallyAvailable' | 'missing';
  /** A film or an episode a library has: play it by its ID. */
  playable: boolean;
  series?: { id: string; name: string };
  seasonNumber?: number;
  episodeNumber?: number;
  /** A series' or a season's episodes, specials aside. */
  episodeCount?: { inLibrary: number; aired: number };
  images?: Images;
  userState?: UserState;
  /** The account's latest request covering it. */
  request?: { id: string; status: RequestStatus };
}

export type RequestStatus = 'pending' | 'approved' | 'declined' | 'processing' | 'partiallyAvailable' | 'available' | 'failed' | (string & {});

export interface MediaRequest {
  id: string;
  title: TitleSummary;
  seasons?: number[];
  status: RequestStatus;
  requestedAt: string;
  /** For the owner: which kids profile made it. */
  profileName?: string;
}

/** A title's page: a series with its seasons, a season with its episodes. */
export interface Title extends TitleSummary {
  overview?: string;
  tagline?: string;
  genres?: string[];
  cast?: { id?: string; name: string; character?: string; image?: ImageRef }[];
  seasons?: TitleSummary[];
  episodes?: TitleSummary[];
}

/** A person, from TMDB, and the titles they're in. */
export interface Person {
  id: string;
  name: string;
  biography?: string;
  birthday?: string;
  deathday?: string;
  birthplace?: string;
  department?: string;
  image?: ImageRef;
  credits: { title: TitleSummary; roles?: string[] }[];
}

export interface TitlePage {
  items: TitleSummary[];
  nextCursor?: string;
  totalCount?: number;
  seed?: string;
}

export interface Playback {
  id: string;
  /** What plays. */
  title: TitleSummary;
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
  /** What to play: the server picks the copy. */
  titleId?: string;
  /** A particular copy, instead. */
  itemId?: string;
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
  readonly titleId: string;
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
  createProfile(profile: { name: string; kind?: 'standard' | 'kids'; movieCeiling?: string; seriesCeiling?: string; language?: string; libraries?: string[] | null; blockedTags?: string[] | null }): Promise<Profile>;
  /** `libraries: null` gives every library again; `blockedTags: null` hides no tag. */
  updateProfile(id: string, changes: { name?: string; language?: string; kind?: 'standard' | 'kids'; movieCeiling?: string; seriesCeiling?: string; libraries?: string[] | null; blockedTags?: string[] | null; pin?: string }): Promise<Profile>;
  deleteProfile(id: string): Promise<null>;
  activateProfile(id: string, pin?: string): Promise<Profile>;
  setPin(id: string, pin: string, ownerPin?: string): Promise<null>;
  removePin(id: string): Promise<null>;
  ratings(): Promise<Ratings>;
  /** The library's tags, to hide some from a profile (owner only). */
  tags(): Promise<string[]>;
  sessions(): Promise<Session[]>;
  endSession(id: string): Promise<null>;
  endOtherSessions(): Promise<null>;

  libraries(): Promise<Library[]>;
  /** A title's page, in a library or not. */
  title(id: string): Promise<Title>;
  person(id: string): Promise<Person>;
  /** A library's titles; kind is required. */
  titles(query?: TitleQuery): Promise<TitlePage>;
  pages(query?: TitleQuery): AsyncGenerator<TitlePage>;
  /** Started titles and each followed series' next episode, a library's or not. */
  continueWatching(limit?: number): Promise<TitleSummary[]>;
  /** A library's genres (every library's without one), for filters. */
  genres(libraryId?: string): Promise<Genre[]>;
  latest(libraryId: string, limit?: number): Promise<TitleSummary[]>;
  imageUrl(ref: ImageRef | undefined, size?: 'small' | 'medium' | 'large'): string;

  discover(list: 'trending' | 'popular' | 'upcoming', mediaKind: 'movie' | 'series',
    options?: { availability?: 'all' | 'library' | 'missing'; limit?: number; cursor?: string }): Promise<TitlePage>;

  setPlayed(id: string, played?: boolean): Promise<UserState>;
  setFavorite(id: string, favorite?: boolean): Promise<UserState>;
  forgetProgress(id: string): Promise<UserState>;
  clearHistory(): Promise<null>;

  watchlist(): Promise<TitleSummary[]>;

  requests(): Promise<MediaRequest[]>;
  requestTitle(titleId: string, seasons?: number[]): Promise<MediaRequest>;
  cancelRequest(id: string): Promise<null>;
  onWatchlist(id: string): Promise<boolean>;
  addToWatchlist(id: string): Promise<null>;
  removeFromWatchlist(id: string): Promise<null>;

  startPlayback(request: PlaybackRequest): Promise<Playback>;
  playback(id: string): Promise<Playback>;
  currentPlayback(): Promise<Playback>;
  reportProgress(id: string, report: { positionMs: number; paused?: boolean; ended?: boolean }): Promise<null>;
  stopPlayback(id: string, positionMs: number, options?: { keepalive?: boolean }): Promise<null>;
  play(video: HTMLVideoElement, titleId: string, options?: PlayerOptions): Promise<Player>;
}
