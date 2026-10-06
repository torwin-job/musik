const $ = (id) => document.getElementById(id);

const WEEKDAY_RU = {
  weekday_mon: "пн",
  weekday_tue: "вт",
  weekday_wed: "ср",
  weekday_thu: "чт",
  weekday_fri: "пт",
  weekday_sat: "сб",
  weekday_sun: "вс",
};

function randomId() {
  // crypto.randomUUID() exists only in a secure context: over http on a LAN
  // address — the setup docs/DEPLOY.md recommends for phones — it is undefined,
  // and every event threw a TypeError. getRandomValues works there, so the
  // fallback keeps the randomness and only formats the UUID by hand.
  if (globalThis.crypto?.randomUUID) {
    return crypto.randomUUID();
  }
  const bytes = new Uint8Array(16);
  if (globalThis.crypto?.getRandomValues) {
    crypto.getRandomValues(bytes);
  } else {
    for (let i = 0; i < bytes.length; i += 1) {
      bytes[i] = Math.floor(Math.random() * 256);
    }
  }
  bytes[6] = (bytes[6] & 0x0f) | 0x40; // version 4
  bytes[8] = (bytes[8] & 0x3f) | 0x80; // RFC 4122 variant
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  return [hex.slice(0, 8), hex.slice(8, 12), hex.slice(12, 16), hex.slice(16, 20), hex.slice(20)].join("-");
}

function clientId() {
  let id = localStorage.getItem("musik_client_id");
  if (!id) {
    id = randomId();
    localStorage.setItem("musik_client_id", id);
  }
  return id;
}

function deviceId() {
  return navigator.userAgentData?.platform || navigator.platform || "web";
}

let sessionId = sessionStorage.getItem("musik_session") || null;
let current = null;
let playlist = [];
let fixedMode = false;
let lastProgressAt = 0;
let listenedAccum = 0;
let lastPos = 0;
let library = [];
let libTab = "tracks";
let libSort = "artist";
let plAddTab = "tracks";
let plAddSort = "artist";
let libTimer = null;
let seeking = false;
let playbackGen = 0;
let toastTimer = null;
let jobPollTimer = null;
let backgroundJobsTimer = null;
let uploadQueue = Promise.resolve();
let uploadQueueDepth = 0;
const knownJobStatuses = new Map();
let favoriteIds = new Set();
let favoriteArtists = new Set();
let favoriteAlbums = new Set(); // "artist\0album"
let homeHydrated = false;
const wiredShelves = new WeakSet();
const shelfAnim = new WeakMap();

function albumKey(artist, album) {
  return `${artist || ""}\0${album || ""}`;
}

let authEnabled = false;
let authReady = false;

function showLogin(message) {
  const gate = $("login-gate");
  if (!gate) return;
  gate.hidden = false;
  document.body.classList.add("locked");
  const err = $("login-error");
  if (message) {
    err.hidden = false;
    err.textContent = message;
  } else {
    err.hidden = true;
  }
  $("login-password")?.focus();
}

function hideLogin() {
  const gate = $("login-gate");
  if (gate) gate.hidden = true;
  document.body.classList.remove("locked");
  $("btn-logout").hidden = !authEnabled;
  $("btn-logout-profile").hidden = !authEnabled;
}

async function api(path, opts = {}) {
  const headers = { "Content-Type": "application/json", ...(opts.headers || {}) };
  const res = await fetch(path, {
    credentials: "same-origin",
    ...opts,
    headers,
  });
  if (res.status === 401 && !path.startsWith("/api/auth/")) {
    showLogin("Нужен вход");
    throw new Error("unauthorized");
  }
  if (!res.ok) {
    let msg = res.statusText;
    try {
      const t = await res.text();
      try {
        const j = JSON.parse(t);
        msg = j.error || t || msg;
      } catch (_) {
        msg = t || msg;
      }
    } catch (_) {}
    throw new Error(msg);
  }
  const ct = res.headers.get("content-type") || "";
  if (ct.includes("json")) return res.json();
  return null;
}

async function ensureAuth() {
  const me = await api("/api/auth/me");
  authEnabled = !!me.auth_enabled;
  if (!authEnabled || me.ok) {
    hideLogin();
    authReady = true;
    return true;
  }
  showLogin();
  authReady = false;
  return false;
}

async function doLogin(password) {
  await api("/api/auth/login", {
    method: "POST",
    body: JSON.stringify({ password }),
  });
  hideLogin();
  authReady = true;
  await bootApp();
}

async function doLogout() {
  await api("/api/auth/logout", { method: "POST", body: "{}" });
  sessionStorage.removeItem("musik_session");
  sessionId = null;
  if (authEnabled) {
    showLogin();
    authReady = false;
  }
}

function escapeHtml(s) {
  return String(s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

function toast(msg) {
  const el = $("toast");
  el.textContent = msg;
  el.hidden = false;
  requestAnimationFrame(() => el.classList.add("show"));
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    el.classList.remove("show");
    setTimeout(() => {
      el.hidden = true;
    }, 220);
  }, 2200);
}

function fmtTime(sec) {
  if (!Number.isFinite(sec) || sec < 0) return "0:00";
  const m = Math.floor(sec / 60);
  const s = Math.floor(sec % 60);
  return `${m}:${String(s).padStart(2, "0")}`;
}

function setSession(id) {
  sessionId = id;
  if (id) sessionStorage.setItem("musik_session", id);
}

function setSeekPct(pct) {
  $("seek").style.setProperty("--seek-pct", `${Math.max(0, Math.min(100, pct))}%`);
}

function setView(name) {
  document.querySelectorAll(".view").forEach((v) => v.classList.remove("active"));
  document.querySelectorAll(".tab[data-view]").forEach((b) => {
    const active = b.dataset.view === name;
    b.classList.toggle("active", active);
    if (active) b.setAttribute("aria-current", "page");
    else b.removeAttribute("aria-current");
  });
  const el = document.getElementById("view-" + name);
  if (el) el.classList.add("active");
  if (name === "home" && !homeHydrated) {
    loadMixes().catch(console.error);
    loadHomeCatalog().catch(console.error);
  }
  if (name === "library") loadLibrary().catch(console.error);
  if (name === "collections") loadPlaylists().catch(console.error);
  if (name === "profile") {
    loadInstalledThemes();
    loadProfile().catch(console.error);
    loadShares().catch(console.error);
    loadContexts().catch(console.error);
    loadRules().catch(console.error);
  }
  window.scrollTo({ top: 0, behavior: "smooth" });
}

function shelfMax(el) {
  return Math.max(0, el.scrollWidth - el.clientWidth);
}

function shelfPos(el) {
  const state = shelfAnim.get(el);
  if (state && state.raf) return state.target;
  return el.scrollLeft;
}

function animateShelfTo(el, target) {
  if (!el) return;
  const max = shelfMax(el);
  target = Math.max(0, Math.min(max, target));
  let state = shelfAnim.get(el);
  if (!state) {
    state = { target: el.scrollLeft, raf: 0 };
    shelfAnim.set(el, state);
  }
  state.target = target;
  updateShelfNav(el);
  if (state.raf) return;

  const tick = () => {
    const cur = el.scrollLeft;
    const diff = state.target - cur;
    if (Math.abs(diff) < 0.75) {
      el.scrollLeft = state.target;
      state.raf = 0;
      updateShelfNav(el);
      return;
    }
    // smooth ease — faster on long jumps, gentle near end
    const t = Math.min(1, Math.abs(diff) / 280);
    const ease = 0.14 + t * 0.18;
    el.scrollLeft = cur + diff * ease;
    updateShelfNav(el);
    state.raf = requestAnimationFrame(tick);
  };
  state.raf = requestAnimationFrame(tick);
}

function nudgeShelf(el, delta) {
  if (!el) return;
  animateShelfTo(el, shelfPos(el) + delta);
}

function updateShelfNav(el) {
  if (!el?.id) return;
  const max = shelfMax(el);
  const pos = shelfPos(el);
  document.querySelectorAll(`.shelf-nav[data-for="${el.id}"]`).forEach((btn) => {
    const isPrev = btn.classList.contains("prev");
    btn.disabled = max < 8 || (isPrev ? pos <= 1 : pos >= max - 1);
  });
}

function wireShelfScroll(el) {
  if (!el || wiredShelves.has(el)) return;
  wiredShelves.add(el);
  shelfAnim.set(el, { target: el.scrollLeft, raf: 0 });

  el.addEventListener(
    "wheel",
    (e) => {
      const max = shelfMax(el);
      if (max < 8) return;
      const dy = e.deltaY;
      const dx = e.deltaX;
      // prefer vertical wheel → horizontal shelf
      const dominant = Math.abs(dy) >= Math.abs(dx) ? dy : dx;
      if (Math.abs(dominant) < 0.2) return;
      e.preventDefault();
      e.stopPropagation();
      // line/page modes → pixel-ish
      let delta = dominant;
      if (e.deltaMode === 1) delta *= 16;
      if (e.deltaMode === 2) delta *= el.clientWidth;
      animateShelfTo(el, shelfPos(el) + delta * 1.35);
    },
    { passive: false }
  );

  el.addEventListener(
    "scroll",
    () => {
      const state = shelfAnim.get(el);
      if (!state?.raf) {
        if (state) state.target = el.scrollLeft;
        updateShelfNav(el);
      }
    },
    { passive: true }
  );

  // resize / content changes
  if (typeof ResizeObserver !== "undefined") {
    const ro = new ResizeObserver(() => updateShelfNav(el));
    ro.observe(el);
  }
  updateShelfNav(el);
}

function wireAllShelves() {
  document.querySelectorAll(".shelf-row").forEach((el) => {
    wireShelfScroll(el);
    // content may have changed width after render
    requestAnimationFrame(() => {
      const state = shelfAnim.get(el);
      if (state && !state.raf) state.target = el.scrollLeft;
      updateShelfNav(el);
    });
  });
  document.querySelectorAll(".shelf-nav").forEach((btn) => {
    if (btn.dataset.wired) return;
    btn.dataset.wired = "1";
    btn.addEventListener("click", (e) => {
      e.preventDefault();
      const el = document.getElementById(btn.dataset.for);
      if (!el) return;
      const step = Math.max(240, Math.round(el.clientWidth * 0.85));
      const dir = btn.classList.contains("prev") ? -1 : 1;
      nudgeShelf(el, dir * step);
    });
  });
}

// Collaborator segments are parsed once at scan time and delivered by the API
// (row.artists). The UI never re-splits a credit — it only shows what it got.
function trackArtists(t) {
  const list = Array.isArray(t?.artists)
    ? t.artists.map((s) => String(s || "").trim()).filter(Boolean)
    : [];
  if (list.length) return list;
  const raw = String(t?.artist || "").trim();
  return raw ? [raw] : [];
}

function primaryArtist(t) {
  return trackArtists(t)[0] || String(t?.artist || "").trim();
}

async function loadFavorites() {
  try {
    const data = await api("/api/favorites");
    favoriteIds = new Set((data.ids || []).map(Number));
    favoriteArtists = new Set(
      (data.artists || []).map((a) => String(a.artist || "").trim()).filter(Boolean)
    );
    favoriteAlbums = new Set(
      (data.albums || [])
        .map((a) => albumKey(String(a.artist || "").trim(), a.album))
        .filter((k) => !k.startsWith("\0"))
    );
    return data;
  } catch (_) {
    favoriteIds = new Set();
    favoriteArtists = new Set();
    favoriteAlbums = new Set();
    return { tracks: [], artists: [], albums: [], ids: [], count: 0 };
  }
}

function setFavoriteUI(on) {
  const btn = $("btn-like");
  if (!btn) return;
  btn.classList.toggle("active-rate", !!on);
  btn.title = on ? "Убрать из любимых песен" : "Любимая песня";
}

function setEntityFavChips() {
  const a = $("btn-fav-artist");
  const b = $("btn-fav-album");
  if (!current) {
    if (a) a.classList.remove("on");
    if (b) b.classList.remove("on");
    return;
  }
  const artistOn = trackArtists(current).some((s) => favoriteArtists.has(s));
  if (a) a.classList.toggle("on", artistOn);
  if (b) b.classList.toggle("on", favoriteAlbums.has(albumKey(primaryArtist(current), current.album)));
}

async function toggleFavorite(payload, { withLike = true } = {}) {
  const body =
    typeof payload === "number" || typeof payload === "string"
      ? { type: "track", track_id: Number(payload) }
      : payload;
  if (!body.type) body.type = "track";
  if (body.type === "artist" || body.type === "album") {
    body.artist = String(body.artist || "").trim();
  }
  const data = await api("/api/favorites/toggle", {
    method: "POST",
    body: JSON.stringify(body),
  });
  if (data.type === "track" || body.type === "track") {
    const id = data.track_id || body.track_id;
    if (data.favorited) favoriteIds.add(id);
    else favoriteIds.delete(id);
    if (current?.id === id) setFavoriteUI(data.favorited);
    toast(data.favorited ? "Любимая песня" : "Песня убрана из любимых");
    if (data.favorited && withLike) postEvent("like").catch(() => {});
  } else if (data.type === "artist" || body.type === "artist") {
    const name = String(data.artist || body.artist || "").trim();
    if (data.favorited) favoriteArtists.add(name);
    else favoriteArtists.delete(name);
    toast(data.favorited ? "Любимый артист" : "Артист убран");
  } else if (data.type === "album" || body.type === "album") {
    const key = albumKey(
      String(data.artist || body.artist || "").trim(),
      data.album || body.album
    );
    if (data.favorited) favoriteAlbums.add(key);
    else favoriteAlbums.delete(key);
    toast(data.favorited ? "Любимый альбом" : "Альбом убран");
  }
  setEntityFavChips();
  loadHomeFavorites().catch(() => {});
  loadSimilarRecs().catch(() => {});
  return data;
}

function groupCatalog(tracks) {
  const artists = new Map();
  const albums = new Map();
  for (const t of tracks) {
    const segments = trackArtists(t);
    for (const artist of segments.length ? segments : ["Unknown"]) {
      if (!artists.has(artist)) {
        artists.set(artist, { artist, tracks: 0, cover: t.artwork || null, sampleId: t.id });
      }
      const a = artists.get(artist);
      a.tracks += 1;
      if (!a.cover && t.artwork) a.cover = t.artwork;
    }

    const album = (t.album || "").trim();
    if (!album) continue;
    const albumArtist = primaryArtist(t) || "Unknown";
    const key = albumArtist + "\0" + album;
    if (!albums.has(key)) {
      albums.set(key, {
        artist: albumArtist,
        album,
        tracks: 0,
        cover: t.artwork || null,
        sampleId: t.id,
      });
    }
    const al = albums.get(key);
    al.tracks += 1;
    if (!al.cover && t.artwork) al.cover = t.artwork;
  }
  return {
    artists: sortArtists([...artists.values()], "tracks"),
    albums: sortAlbums([...albums.values()], "tracks"),
  };
}

function cmpText(a, b) {
  return String(a || "").localeCompare(String(b || ""), "ru", { sensitivity: "base" });
}

function sortTracks(list, sort) {
  const out = [...list];
  out.sort((a, b) => {
    if (sort === "title") return cmpText(a.title, b.title) || cmpText(a.artist, b.artist);
    if (sort === "album") return cmpText(a.album, b.album) || cmpText(a.artist, b.artist) || cmpText(a.title, b.title);
    return cmpText(a.artist, b.artist) || cmpText(a.album, b.album) || cmpText(a.title, b.title);
  });
  return out;
}

function sortArtists(list, sort) {
  const out = [...list];
  out.sort((a, b) => {
    if (sort === "tracks") return (b.tracks || 0) - (a.tracks || 0) || cmpText(a.artist, b.artist);
    return cmpText(a.artist, b.artist);
  });
  return out;
}

function sortAlbums(list, sort) {
  const out = [...list];
  out.sort((a, b) => {
    if (sort === "tracks") return (b.tracks || 0) - (a.tracks || 0) || cmpText(a.album, b.album);
    if (sort === "artist") return cmpText(a.artist, b.artist) || cmpText(a.album, b.album);
    return cmpText(a.album, b.album) || cmpText(a.artist, b.artist);
  });
  return out;
}

function catalogSortOptions(tab) {
  if (tab === "artists") {
    return [
      ["name", "по имени"],
      ["tracks", "у кого больше песен"],
    ];
  }
  if (tab === "albums") {
    return [
      ["album", "по альбому"],
      ["artist", "по артисту"],
      ["tracks", "по числу песен"],
    ];
  }
  return [
    ["artist", "по артисту"],
    ["title", "по песне"],
    ["album", "по альбому"],
  ];
}

function fillSortSelect(el, tab, current) {
  if (!el) return current;
  const opts = catalogSortOptions(tab);
  const allowed = new Set(opts.map((o) => o[0]));
  const value = allowed.has(current) ? current : opts[0][0];
  el.innerHTML = opts.map(([v, label]) => `<option value="${v}">${label}</option>`).join("");
  el.value = value;
  return value;
}

function tracksOfArtist(artist) {
  const name = (artist || "Unknown").trim() || "Unknown";
  return library.filter((t) =>
    trackArtists(t).some((seg) => seg.toLowerCase() === name.toLowerCase())
  );
}

function tracksOfAlbum(artist, album) {
  const a = (artist || "").trim().toLowerCase();
  const al = (album || "").trim();
  return library.filter((t) => {
    if ((t.album || "").trim() !== al) return false;
    return trackArtists(t).some((seg) => seg.toLowerCase() === a);
  });
}

function coverImgHTML(src, width = 256, height = 256) {
  if (!src) return "";
  return `<img src="${escapeHtml(src)}" alt="" width="${width}" height="${height}" loading="lazy" decoding="async" onerror="this.remove()">`;
}

function entityCoverHtml(cover, letter, round) {
  if (cover) {
    return `<div class="entity-art${round ? " round" : ""}">${coverImgHTML(thumbURL(cover, 256))}</div>`;
  }
  return `<div class="letter">${escapeHtml((letter || "♪").slice(0, 1).toUpperCase())}</div>`;
}

function tileArtHTML(cover, letter) {
  if (cover) {
    return `<div class="tile-art">${coverImgHTML(thumbURL(cover, 256))}</div>`;
  }
  return `<div class="letter">${escapeHtml((letter || "♪").slice(0, 1))}</div>`;
}

function setCoverImg(img, src, host) {
  if (!img) return;
  if (!src) {
    img.removeAttribute("src");
    host?.classList.remove("has-art");
    return;
  }
  const show = () => host?.classList.add("has-art");
  img.onload = show;
  img.onerror = () => {
    img.removeAttribute("src");
    host?.classList.remove("has-art");
  };
  if (img.getAttribute("src") === src) {
    if (img.complete && img.naturalWidth) show();
    return;
  }
  host?.classList.remove("has-art");
  img.src = src;
}

function renderMaturity(m) {
  const el = $("maturity");
  if (!el) return;
  const map = {
    discovering: "изучаем вкус · слушай и скипай",
    forming: "вкус формируется",
    ready: "вкус готов",
  };
  el.textContent = map[m] || "твой локальный микс";
}

function setRatingUI(rating) {
  const dislike = $("btn-dislike");
  if (!dislike) return;
  dislike.classList.toggle("active-rate", rating === "dislike");
  dislike.disabled = rating === "dislike";
  // heart = favorites state (not session like lock)
  if (current?.id) setFavoriteUI(favoriteIds.has(current.id));
}

const PLAY_ICON = `<svg class="glyph" viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M8.75 5.5v13l10-6.5z"/></svg>`;
const PAUSE_ICON = `<svg class="glyph" viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M6.8 5.5h3.4v13H6.8zm7 0h3.4v13h-3.4z"/></svg>`;

function setPlayIcon(playing) {
  const icon = playing ? PAUSE_ICON : PLAY_ICON;
  $("btn-play").innerHTML = icon;
  $("mini-play").innerHTML = icon;
}

function updateMini(track) {
  const mini = $("mini");
  if (!track) {
    mini.hidden = true;
    return;
  }
  mini.hidden = false;
  $("mini-title").textContent = track.title || "—";
  $("mini-artist").textContent = track.artist || "";
  setCoverImg($("mini-art-img"), track.artwork ? thumbURL(track.artwork, 96) : "", $("mini-art"));
}

function applyPlayPayload(data, { autoplay = true } = {}) {
  if (data.session_id) setSession(data.session_id);
  if (data.maturity) renderMaturity(data.maturity);
  fixedMode = !!data.fixed || (Array.isArray(data.tracks) && data.tracks.length > 0);
  if (data.name || data.mode) {
    $("mode-label").textContent = data.name || data.mode || "";
  }
  if (Array.isArray(data.tracks)) {
    playlist = data.tracks;
    renderPlaylist(playlist, data.index);
    $("queue").hidden = true;
    $("playlist").hidden = false;
  } else if (data.queue) {
    playlist = [];
    renderQueue(data.queue);
    $("playlist").hidden = true;
    $("queue").hidden = false;
  }
  if (data.current) {
    if (autoplay) renderNow(data.current);
    else {
      current = data.current;
      updateMini(data.current);
      $("title").textContent = data.current.title || "—";
      $("artist").textContent = [data.current.artist, data.current.album].filter(Boolean).join(" · ");
      setNowSource(data.current.source);
    }
  }
}

function renderNow(track) {
  current = track;
  const art = $("art");
  if (!track) {
    $("title").textContent = "Выбери микс";
    $("artist").textContent = "или трек в библиотеке";
    art.classList.remove("has-art");
    setCoverImg($("art-img"), "", art);
    setNowSource("");
    updateMini(null);
    setPlayIcon(false);
    return;
  }
  $("title").textContent = track.title || "#" + track.id;
  $("artist").textContent = [track.artist, track.album].filter(Boolean).join(" · ");
  setNowSource(track.source);
  $("art-fallback").textContent = (track.title || "♪").slice(0, 1).toUpperCase();
  if (track.artwork) {
    setCoverImg($("art-img"), thumbURL(track.artwork, 640), art);
  } else {
    setCoverImg($("art-img"), "", art);
  }
  updateMini(track);
  const audio = $("audio");
  const url = track.stream || `/api/stream/${track.id}`;
  if (audio.dataset.trackId !== String(track.id)) {
    const gen = bumpPlayback();
    audio.dataset.trackId = String(track.id);
    audio.dataset.gen = String(gen);
    audio.src = url;
    audio.play().then(() => setPlayIcon(true)).catch(() => setPlayIcon(false));
    listenedAccum = 0;
    lastPos = 0;
    $("seek").value = 0;
    setSeekPct(0);
    $("time-cur").textContent = "0:00";
    $("time-dur").textContent = fmtTime(track.duration || 0);
    setRatingUI(null);
    setFavoriteUI(favoriteIds.has(track.id));
    setEntityFavChips();
    postEvent("track_start", { track_id: track.id }).catch(() => {});
    loadLyrics(track.id);
  } else {
    setFavoriteUI(favoriteIds.has(track.id));
    setEntityFavChips();
  }
  highlightPlaylist(track.id);
}

async function loadLyrics(trackId) {
  const el = $("lyrics-text");
  if (!el) return;
  el.textContent = "…";
  try {
    const ly = await api(`/api/tracks/${trackId}/lyrics`);
    if (!ly || ly.status === "absent" || ly.status === "missing") {
      el.textContent = "Текста нет (musik lyrics)";
      return;
    }
    if (ly.instrumental) {
      el.textContent = "(instrumental)";
      return;
    }
    el.textContent = ly.plain_lyrics || ly.synced_lyrics || "—";
  } catch {
    el.textContent = "не удалось загрузить";
  }
}

function bumpPlayback() {
  playbackGen += 1;
  return playbackGen;
}

function trackArtURL(t) {
  if (t?.artwork) return thumbURL(t.artwork, 96);
  const id = t?.id || t?.track_id;
  return id ? `/api/artwork/${id}?w=96` : "";
}

function queueRowHTML(t, i, { why = false, now = false } = {}) {
  const id = t.id || t.track_id;
  const art = trackArtURL(t);
  const letter = escapeHtml((t.title || t.artist || "?").slice(0, 1).toUpperCase());
  const reason = why ? whyLabel(t) : "";
  return `
    <span class="q-art" data-letter="${letter}">${
      art ? `<img src="${escapeHtml(art)}" alt="" width="96" height="96" loading="lazy" decoding="async" onerror="this.remove()">` : ""
    }</span>
    <span class="q-body">
      <strong>${escapeHtml(t.title || "#" + id)}</strong>
      <span class="meta">
        ${escapeHtml(t.artist || "")}
        ${reason ? `<em class="q-why">${escapeHtml(reason)}</em>` : ""}
      </span>
    </span>
    <span class="dur">${fmtTime(t.duration || 0)}</span>
    <span class="pos">${now ? "▶" : i + 1}</span>`;
}

function renderQueue(queue) {
  const ol = $("queue");
  ol.innerHTML = "";
  const list = queue || [];
  $("playlist-label").textContent = "Дальше в радио";
  setQueueHint("Нажми песню — сразу она");
  $("queue-count").textContent = list.length ? `${list.length}` : "";
  list.forEach((q, i) => {
    const id = q.track_id || q.id;
    const li = document.createElement("li");
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "playlist-item";
    if (id) btn.dataset.trackId = String(id);
    btn.innerHTML = queueRowHTML(q, i, { why: true });
    bindTrackButton(btn, () => jumpTo(id, i).catch((e) => toast(e.message || String(e))));
    li.appendChild(btn);
    ol.appendChild(li);
  });
}

function renderPlaylist(tracks, currentIndex) {
  const ol = $("playlist");
  ol.innerHTML = "";
  const list = tracks || [];
  $("playlist-label").textContent = "В этом списке";
  setQueueHint("Нажми песню — сразу она");
  $("queue-count").textContent = list.length ? `${list.length}` : "";
  list.forEach((t, i) => {
    const id = t.id || t.track_id;
    const li = document.createElement("li");
    const btn = document.createElement("button");
    const now = !!(t.current || i === currentIndex || (current && id === current.id));
    btn.type = "button";
    btn.className = "playlist-item";
    if (id) btn.dataset.trackId = String(id);
    if (now) btn.classList.add("current");
    btn.innerHTML = queueRowHTML(t, i, { now });
    bindTrackButton(btn, () => jumpTo(id, t.position ?? i).catch((e) => toast(e.message || String(e))));
    li.appendChild(btn);
    ol.appendChild(li);
  });
}

function highlightPlaylist(trackId) {
  const ol = $("playlist");
  if (!ol || ol.hidden) return;
  ol.querySelectorAll(".playlist-item").forEach((btn, i) => {
    const on = Number(btn.dataset.trackId) === trackId;
    btn.classList.toggle("current", on);
    const pos = btn.querySelector(".pos");
    if (pos) pos.textContent = on ? "▶" : String(i + 1);
  });
}

async function postEvent(type, extra = {}) {
  if (!sessionId && type !== "track_start") {
    toast("Сначала запусти микс или трек");
    throw new Error("no session");
  }
  const startedGen = playbackGen;
  const audio = $("audio");
  const body = {
    type,
    event_id: extra.event_id || randomId(),
    track_id: current?.id,
    session_id: sessionId,
    impression_id: extra.impression_id || current?.impression_id,
    client_id: clientId(),
    device_id: deviceId(),
    position_sec: audio.currentTime || 0,
    duration_sec: audio.duration || current?.duration || 0,
    listened_sec: listenedAccum,
    ...extra,
  };
  const data = await api("/api/events", { method: "POST", body: JSON.stringify(body) });
  if (data.session_id) setSession(data.session_id);
  if (data.maturity) renderMaturity(data.maturity);
  if (data.name) $("mode-label").textContent = data.name;
  const refreshList = type === "skip" || type === "track_end" || type === "like" || type === "dislike";
  if (refreshList) {
    if (Array.isArray(data.tracks)) {
      playlist = data.tracks;
      renderPlaylist(playlist, data.index);
      $("queue").hidden = true;
      $("playlist").hidden = false;
    } else if (data.queue) {
      renderQueue(data.queue);
      $("playlist").hidden = true;
      $("queue").hidden = false;
    }
  }
  if (data.next) {
    if (startedGen !== playbackGen) return data;
    renderNow(data.next);
    setRatingUI(null);
  } else if (data.ended) {
    setPlayIcon(false);
    toast("Конец плейлиста");
  }
  if (type === "dislike") {
    if (data.ignored) toast("Уже дизлайк");
    else toast("Дизлайк");
    setRatingUI(data.rating || type);
  }
  return data;
}

async function startRadio(seed) {
  const body = seed ? { seed_track_id: seed } : {};
  const data = await api("/api/radio/start", { method: "POST", body: JSON.stringify(body) });
  fixedMode = false;
  playlist = [];
  $("playlist").hidden = true;
  $("queue").hidden = false;
  setSession(data.session_id);
  $("mode-label").textContent = "радио";
  renderMaturity(data.maturity);
  renderQueue(data.queue);
  renderNow(data.current);
  setView("player");
}

async function playFixed(body) {
  const data = await api("/api/play", { method: "POST", body: JSON.stringify(body) });
  applyPlayPayload(data);
  setView("player");
  return data;
}

async function playMix(kind, cardEl, opts = {}) {
  if (cardEl) cardEl.classList.add("busy");
  try {
    const data = await api(`/api/mixes/${encodeURIComponent(kind)}/play`, {
      method: "POST",
      body: JSON.stringify(opts),
    });
    applyPlayPayload(data);
    setView("player");
  } finally {
    if (cardEl) cardEl.classList.remove("busy");
  }
}

function bindTrackButton(btn, onClick) {
  let startY = 0;
  let moved = false;
  btn.addEventListener("pointerdown", (e) => {
    startY = e.clientY;
    moved = false;
  });
  btn.addEventListener("pointermove", (e) => {
    if (Math.abs(e.clientY - startY) > 8) moved = true;
  });
  btn.addEventListener("click", (e) => {
    if (moved) {
      e.preventDefault();
      return;
    }
    onClick();
  });
}

async function jumpTo(trackId, index) {
  if (!sessionId) throw new Error("no session");
  if (current && Number(trackId) === Number(current.id)) {
    return;
  }
  bumpPlayback();
  const audio = $("audio");
  audio.pause();
  const data = await api("/api/session/jump", {
    method: "POST",
    body: JSON.stringify({
      session_id: sessionId,
      track_id: trackId,
      index: typeof index === "number" ? index : undefined,
    }),
  });
  // force reload even if same src logic — clear dataset
  $("audio").dataset.trackId = "";
  applyPlayPayload(data);
}

function thumbURL(url, width = 256) {
  if (!url) return "";
  const u = String(url);
  if (/[?&]w=/.test(u)) return u;
  return u.includes("?") ? `${u}&w=${width}` : `${u}?w=${width}`;
}

function coverStyle(m) {
  if (m.cover_track_id) {
    return `<div class="cover-photo">${coverImgHTML(`/api/artwork/${m.cover_track_id}?w=256`)}</div>`;
  }
  const blobs = ["blob-a", "blob-b", "blob-c", "blob-d"];
  const b = blobs[(m.kind || "").length % blobs.length];
  return `<div class="cover-blob ${b}"></div>`;
}

function metaLabel(m) {
  if (m.kind === "later") return m.tracks ? `${m.tracks} в очереди` : "пусто — добавь с плеера";
  if (m.kind === "favorites") return m.tracks ? `${m.tracks} ♥` : "жми ♥ на треке";
  if (!m.ready) return "нажми «Обновить миксы»";
  return `${m.tracks} треков`;
}

async function loadMixes() {
  const main = $("mix-cards");
  const week = $("weekday-cards");
  main.innerHTML = '<div class="skeleton"></div><div class="skeleton"></div><div class="skeleton"></div>';
  week.innerHTML = "";

  const data = await api("/api/mixes");
  main.innerHTML = "";
  const todayKey = data.today_weekday;
  $("mix-hint").textContent = todayKey
    ? `сегодня · ${WEEKDAY_RU[todayKey] || todayKey}`
    : "";

  (data.mixes || []).forEach((m) => {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "mix-card";
    if (m.kind === "for_you" || m.kind === "daily" || m.kind === "favorites") btn.classList.add("highlight");
    if (m.kind === "new_releases") btn.classList.add("tone-b");
    if (m.kind === "later") btn.classList.add("tone-c");
    if (m.kind === "favorites") btn.classList.add("tone-fav");
    if (m.today) btn.classList.add("today-card", "highlight");
    if (!m.ready && m.kind !== "later" && m.kind !== "favorites") btn.classList.add("dim");
    btn.innerHTML = `
      ${coverStyle(m)}
      <strong>${escapeHtml(m.title)}</strong>
      <span>${escapeHtml(m.subtitle || "")}</span>
      <div class="mix-meta">${escapeHtml(metaLabel(m))}</div>`;
    btn.onclick = () => {
      playMix(m.kind, btn).catch((e) => toast(e.message || String(e)));
    };
    if (String(m.kind).startsWith("weekday_")) week.appendChild(btn);
    else main.appendChild(btn);
  });
  wireAllShelves();
}

async function ensureLibrary() {
  if (library.length) return library;
  library = await api("/api/library");
  return library;
}

function makeHeart(on, onClick) {
  const h = document.createElement("button");
  h.type = "button";
  h.className = "card-heart" + (on ? " on" : "");
  h.textContent = "♥";
  h.title = on ? "Убрать из любимых" : "В любимые";
  h.onclick = (e) => {
    e.stopPropagation();
    onClick();
  };
  return h;
}

function renderEntityShelf(el, items, kind) {
  if (!el) return;
  el.innerHTML = "";
  items.forEach((item) => {
    const btn = document.createElement("button");
    btn.type = "button";
    if (kind === "artist") {
      const on = favoriteArtists.has(item.artist);
      btn.className = "mix-card entity-card artist-card";
      btn.innerHTML = `
        ${entityCoverHtml(item.cover || item.artwork, item.artist, true)}
        <strong>${escapeHtml(item.artist)}</strong>
        <span>артист</span>
        <div class="mix-meta">${item.tracks} треков</div>`;
      btn.appendChild(
        makeHeart(on, () =>
          toggleFavorite({ type: "artist", artist: item.artist }, { withLike: false }).then(() =>
            renderEntityShelf(el, items, kind)
          )
        )
      );
      btn.onclick = () =>
        playFixed({ artist: item.artist }).catch((e) => toast(e.message || String(e)));
    } else if (kind === "album") {
      const on = favoriteAlbums.has(albumKey(item.artist, item.album));
      btn.className = "mix-card entity-card";
      btn.innerHTML = `
        ${entityCoverHtml(item.cover || item.artwork, item.album)}
        <strong>${escapeHtml(item.album)}</strong>
        <span>${escapeHtml(item.artist)}</span>
        <div class="mix-meta">${item.tracks || ""} ${item.explanation ? "" : "треков"}</div>
        ${item.explanation ? `<div class="why-line">${escapeHtml(item.explanation)}</div>` : ""}`;
      btn.appendChild(
        makeHeart(on, () =>
          toggleFavorite(
            { type: "album", artist: item.artist, album: item.album },
            { withLike: false }
          ).then(() => renderEntityShelf(el, items, kind))
        )
      );
      btn.onclick = () =>
        playFixed({ artist: item.artist, album: item.album }).catch((e) =>
          toast(e.message || String(e))
        );
    } else {
      btn.className = "mix-card entity-card track-card";
      const id = item.id || item.track_id;
      const fav = favoriteIds.has(id);
      btn.innerHTML = `
        ${entityCoverHtml(item.artwork, item.title)}
        <strong>${escapeHtml(item.title)}</strong>
        <span>${escapeHtml(item.artist || "")}</span>
        <div class="mix-meta">${fav ? "♥ " : ""}${
          item.explanation ? escapeHtml(item.explanation) : fmtTime(item.duration || 0)
        }</div>`;
      btn.appendChild(
        makeHeart(fav, () =>
          toggleFavorite({ type: "track", track_id: id }, { withLike: false }).then(() =>
            renderEntityShelf(el, items, kind)
          )
        )
      );
      btn.onclick = () =>
        playFixed({ track_id: id, name: item.title }).catch((e) =>
          toast(e.message || String(e))
        );
    }
    el.appendChild(btn);
  });
  requestAnimationFrame(() => updateShelfNav(el));
}

function emptyShelfCard(el, title, sub) {
  el.innerHTML = "";
  const empty = document.createElement("button");
  empty.type = "button";
  empty.className = "mix-card entity-card track-card dim";
  empty.innerHTML = `
    <div class="cover-blob blob-b"></div>
    <strong>${escapeHtml(title)}</strong>
    <span>${escapeHtml(sub)}</span>
    <div class="mix-meta">любимое</div>`;
  el.appendChild(empty);
  updateShelfNav(el);
}

async function loadHomeFavorites() {
  const data = await loadFavorites();
  const hint = $("fav-hint");
  const c = data.counts || {};
  if (hint) {
    hint.textContent = `${c.tracks || 0} песен · ${c.artists || 0} артистов · ${c.albums || 0} альбомов`;
  }

  const songs = $("home-favorites");
  if (songs) {
    if (!data.tracks?.length) {
      emptyShelfCard(songs, "Пока пусто", "Жми ♥ в плеере — песня появится здесь");
    } else {
      const items = data.tracks.map((row) => {
        const t = row.track || row;
        return {
          id: t.id || row.track_id,
          title: t.title,
          artist: t.artist || row.artist,
          artwork: t.artwork,
          duration: t.duration || row.duration,
        };
      });
      renderEntityShelf(songs, items, "track");
      // play as favorites mix when clicking — override
      [...songs.querySelectorAll(".mix-card")].forEach((btn, i) => {
        const id = items[i].id;
        btn.onclick = () =>
          playMix("favorites", null, { track_id: id }).catch((e) =>
            toast(e.message || String(e))
          );
      });
    }
  }

  const arts = $("home-fav-artists");
  if (arts) {
    if (!data.artists?.length) {
      emptyShelfCard(arts, "Нет любимых артистов", "♥ на карточке артиста");
    } else {
      renderEntityShelf(
        arts,
        data.artists.map((a) => ({
          artist: a.artist,
          tracks: a.tracks,
          cover: a.artwork,
        })),
        "artist"
      );
    }
  }

  const albs = $("home-fav-albums");
  if (albs) {
    if (!data.albums?.length) {
      emptyShelfCard(albs, "Нет любимых альбомов", "♥ на карточке альбома");
    } else {
      renderEntityShelf(
        albs,
        data.albums.map((a) => ({
          artist: a.artist,
          album: a.album,
          tracks: a.tracks,
          cover: a.artwork,
        })),
        "album"
      );
    }
  }
}

async function loadSimilarRecs() {
  const el = $("home-similar");
  const hint = $("rec-hint");
  if (!el) return;
  const data = await api("/api/recommend/favorites");
  if (hint) {
    hint.textContent = data.empty
      ? "сначала добавь любимое"
      : data.explanation || "по звучанию";
  }
  if (data.empty || !data.tracks?.length) {
    emptyShelfCard(el, "Мало данных", "Добавь любимые песни / артистов / альбомы");
    return;
  }
  // mix tracks + a few artists/albums in one shelf
  const items = [];
  (data.tracks || []).slice(0, 16).forEach((t) => items.push({ ...t, _kind: "track" }));
  (data.artists || []).slice(0, 6).forEach((a) =>
    items.push({
      artist: a.artist,
      tracks: a.tracks,
      cover: a.artwork,
      explanation: a.explanation,
      _kind: "artist",
    })
  );
  (data.albums || []).slice(0, 6).forEach((a) =>
    items.push({
      artist: a.artist,
      album: a.album,
      tracks: a.tracks,
      cover: a.artwork,
      explanation: a.explanation,
      _kind: "album",
    })
  );
  el.innerHTML = "";
  items.forEach((item) => {
    if (item._kind === "artist") {
      const wrap = document.createElement("div");
      wrap.style.display = "contents";
      renderEntityShelf(el, [item], "artist");
    } else if (item._kind === "album") {
      renderEntityShelf(el, [item], "album");
    } else {
      renderEntityShelf(el, [item], "track");
    }
  });
  // re-render cleanly once
  el.innerHTML = "";
  items.forEach((item) => {
    const tmp = document.createElement("div");
    if (item._kind === "artist") renderEntityShelf(tmp, [item], "artist");
    else if (item._kind === "album") renderEntityShelf(tmp, [item], "album");
    else renderEntityShelf(tmp, [item], "track");
    while (tmp.firstChild) el.appendChild(tmp.firstChild);
  });
  requestAnimationFrame(() => updateShelfNav(el));
}

async function loadHomeCatalog() {
  const favsP = loadFavorites();
  const [artistsRes, albumsRes, tracks] = await Promise.all([
    api("/api/artists?limit=28"),
    api("/api/albums?limit=28"),
    api("/api/library?limit=36"),
  ]);
  await favsP;
  renderEntityShelf($("home-artists"), artistsRes.artists || [], "artist");
  renderEntityShelf($("home-albums"), albumsRes.albums || [], "album");
  renderEntityShelf($("home-tracks"), Array.isArray(tracks) ? tracks : [], "track");
  homeHydrated = true;
  wireAllShelves();
  loadHomeFavorites().catch(console.error);
  loadSimilarRecs().catch(console.error);
  ensureLibrary().catch(console.error);
}

async function showTips(kind) {
  const path = kind === "new" ? "/api/discover/albums" : "/api/discover/resurfaced";
  const panel = $("tips-panel");
  panel.classList.remove("hidden");
  panel.innerHTML = '<p class="sub">Загрузка…</p>';
  const data = await api(path);
  panel.innerHTML = "";
  if (!data.tips?.length) {
    panel.innerHTML = '<p class="sub">Пусто. Обнови миксы или добавь музыку в библиотеку.</p>';
    return;
  }
  data.tips.forEach((t) => {
    const div = document.createElement("div");
    div.className = "tip";
    const ids = t.track_ids || [];
    div.innerHTML = `<div>
        <h4>${escapeHtml(t.artist || "")} — ${escapeHtml(t.album || "")}</h4>
        <p>${escapeHtml(t.explanation || "")}</p>
      </div>
      <button type="button" class="btn primary">Слушать альбом</button>`;
    div.querySelector("button").onclick = () => {
      if (!ids.length) return;
      playFixed({
        track_ids: ids,
        name: `${t.artist || ""} — ${t.album || "альбом"}`.trim(),
      }).catch((e) => toast(e.message || String(e)));
    };
    panel.appendChild(div);
  });
}

function setLibTab(tab) {
  libTab = tab;
  document.querySelectorAll("#view-library .seg-btn").forEach((b) => {
    const active = b.dataset.lib === tab;
    b.classList.toggle("active", active);
    b.setAttribute("aria-selected", String(active));
  });
  const ph = $("lib-filter");
  if (ph) {
    ph.placeholder =
      tab === "artists"
        ? "Поиск артиста…"
        : tab === "albums"
          ? "Поиск альбома…"
          : tab === "favorites"
            ? "Поиск в избранном…"
            : "Артист, трек, альбом…";
  }
  libSort = fillSortSelect($("lib-sort"), tab, libSort);
  renderLib(ph?.value || "");
}

async function loadLibrary() {
  library = await api("/api/library");
  await loadFavorites();
  const { artists, albums } = groupCatalog(library);
  $("lib-count").textContent = `${library.length} треков · ${artists.length} артистов · ${albums.length} альбомов · ${favoriteIds.size} ♥`;
  setLibTab(libTab);
}

function renderLib(q) {
  const qq = q.trim().toLowerCase();
  const ul = $("lib-list");
  const grid = $("lib-grid");
  if (!ul || !grid) return;

  if (libTab === "tracks" || libTab === "favorites") {
    ul.hidden = false;
    grid.hidden = true;
    grid.innerHTML = "";
    ul.innerHTML = "";
    const source =
      libTab === "favorites" ? library.filter((t) => favoriteIds.has(t.id)) : library;
    if (libTab === "tracks" && !source.length && !qq) {
      ul.innerHTML =
        '<li class="sub" style="padding:.8rem 0">Пока пусто. Добавь музыку во вкладке «Загрузка».</li>';
      return;
    }
    if (libTab === "favorites" && !source.length) {
      ul.innerHTML = '<li class="sub" style="padding:.8rem 0">Избранное пусто — жми ♥ в плеере</li>';
      return;
    }
    sortTracks(
      source.filter((t) => !qq || `${t.artist} ${t.title} ${t.album}`.toLowerCase().includes(qq)),
      libSort
    )
      .slice(0, 400)
      .forEach((t) => {
        const li = document.createElement("li");
        li.className = "track-row";
        const isFav = favoriteIds.has(t.id);
        li.innerHTML = `
          <button type="button" class="linkish">${isFav ? "♥ " : ""}${t.ready === false ? "… " : ""}${escapeHtml(t.artist)} — ${escapeHtml(t.title)}</button>
          <span class="dur">${fmtTime(t.duration || 0)}</span>
          <span class="row-actions">
            <button type="button" class="tiny" data-act="track">Трек</button>
            <button type="button" class="tiny" data-act="fav">${isFav ? "Убрать ♥" : "♥"}</button>
            <button type="button" class="tiny" data-act="album">Альбом</button>
            <button type="button" class="tiny" data-act="artist">Артист</button>
            <button type="button" class="tiny" data-act="later">Потом</button>
            <button type="button" class="tiny" data-act="playlist">Плейлист</button>
            <button type="button" class="tiny" data-act="radio">Радио</button>
          </span>`;
        li.querySelector(".linkish").onclick = () =>
          playFixed({ track_id: t.id, name: t.title }).catch((e) => toast(e.message || String(e)));
        li.querySelectorAll("[data-act]").forEach((btn) => {
          btn.onclick = async (e) => {
            e.stopPropagation();
            const act = btn.dataset.act;
            try {
              if (act === "track") await playFixed({ track_id: t.id, name: t.title });
              else if (act === "fav") {
                await toggleFavorite({ type: "track", track_id: t.id }, { withLike: false });
                renderLib($("lib-filter").value || "");
              } else if (act === "album") {
                if (!t.album) return toast("У трека нет альбома");
                await playFixed({ artist: t.artist, album: t.album });
              } else if (act === "artist") {
                if (!t.artist) return toast("Нет артиста");
                await playFixed({ artist: t.artist });
              } else if (act === "later") {
                await api("/api/later", { method: "POST", body: JSON.stringify({ track_id: t.id }) });
                toast("В «Потом»");
              } else if (act === "playlist") {
                await pickPlaylistForTrack(t.id);
              } else if (act === "radio") {
                await startRadio(t.id);
              }
            } catch (err) {
              toast(err.message || String(err));
            }
          };
        });
        ul.appendChild(li);
      });
    return;
  }

  ul.hidden = true;
  ul.innerHTML = "";
  grid.hidden = false;
  grid.innerHTML = "";
  const { artists, albums } = groupCatalog(library);

  if (libTab === "artists") {
    sortArtists(
      artists.filter((a) => !qq || a.artist.toLowerCase().includes(qq)),
      libSort
    ).forEach((a) => {
        const btn = document.createElement("button");
        btn.type = "button";
        btn.className = "lib-tile artist";
        btn.innerHTML = `
          ${tileArtHTML(a.cover, a.artist)}
          <strong>${escapeHtml(a.artist)}</strong>
          <span>${a.tracks} треков</span>
          <div class="mix-meta">
            слушать
            <span class="tiny tile-pl" data-act="playlist">в плейлист</span>
          </div>`;
        btn.onclick = (e) => {
          if (e.target.closest("[data-act=playlist]")) {
            e.preventDefault();
            pickPlaylistForTracks(tracksOfArtist(a.artist).map((t) => t.id)).catch((err) =>
              toast(err.message || String(err))
            );
            return;
          }
          playFixed({ artist: a.artist }).catch((err) => toast(err.message || String(err)));
        };
        grid.appendChild(btn);
      });
    return;
  }

  sortAlbums(
    albums.filter((al) => !qq || `${al.artist} ${al.album}`.toLowerCase().includes(qq)),
    libSort
  ).forEach((al) => {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "lib-tile";
      btn.innerHTML = `
        ${tileArtHTML(al.cover, al.album)}
        <strong>${escapeHtml(al.album)}</strong>
        <span>${escapeHtml(al.artist)}</span>
        <div class="mix-meta">
          ${al.tracks} треков
          <span class="tiny tile-pl" data-act="playlist">в плейлист</span>
        </div>`;
      btn.onclick = (e) => {
        if (e.target.closest("[data-act=playlist]")) {
          e.preventDefault();
          pickPlaylistForTracks(tracksOfAlbum(al.artist, al.album).map((t) => t.id)).catch((err) =>
            toast(err.message || String(err))
          );
          return;
        }
        playFixed({ artist: al.artist, album: al.album }).catch((err) => toast(err.message || String(err)));
      };
      grid.appendChild(btn);
    });
}

const SOURCE_LABELS = {
  exploit: "похоже на тебя",
  transition: "часто после этой",
  explore_adjacent: "чуть в сторону",
  resurface: "давно не звучало",
  new_in_library: "новое в библиотеке",
  wildcard: "для разнообразия",
  explore: "чуть в сторону",
  radio_start: "старт радио",
  refill: "добор очереди",
  manual: "ты выбрал",
};

function sourceLabel(source) {
  if (source === "aggregate") return "общая доля";
  return SOURCE_LABELS[source] || source || "—";
}

function whyLabel(item) {
  const reason = sourceLabel(item?.source);
  if (item?.new_boost && item.source !== "new_in_library") {
    return `${reason} · свежее`;
  }
  return reason;
}

function setQueueHint(text) {
  const el = $("queue-hint");
  if (el) el.textContent = text;
}

function setNowSource(source) {
  const sourceEl = $("now-source");
  if (!sourceEl) return;
  if (source) {
    sourceEl.hidden = false;
    sourceEl.className = `tag source-${source}`;
    sourceEl.textContent = sourceLabel(source);
    return;
  }
  sourceEl.hidden = true;
  sourceEl.textContent = "";
  sourceEl.className = "tag";
}

function pct(value) {
  return `${Math.round((Number(value) || 0) * 100)}%`;
}

async function loadProfile() {
  const [p, week, rec] = await Promise.all([
    api("/api/profile"),
    api("/api/metrics/weekly"),
    api("/api/metrics/recommendations"),
  ]);
  renderMaturity(p.maturity);
  renderTasteCard(p);
  renderMonitor(p, week, rec);
  bindExploreBounds();
  loadShares().catch(console.error);
}

function renderTasteCard(p) {
  const readyAt = Math.max(1, p.ready_at || 8);
  const likes = p.n_positive || 0;
  const skips = p.n_negative || 0;
  const progress = Math.min(100, Math.round((likes / readyAt) * 100));
  const status = tasteStatus(p.maturity);
  const artists = (p.top_artists || []).map((a) =>
    `<span class="artist-chip">${escapeHtml(a.artist)}</span>`
  ).join("") || `<span class="profile-detail">Появятся после нескольких прослушиваний</span>`;
  $("profile-box").innerHTML = `
    <div class="panel-kicker">вкус</div>
    <div class="monitor-title">
      <strong>${escapeHtml(status.title)}</strong>
      <span class="chip ${p.maturity === "ready" ? "on" : ""}">${escapeHtml(status.chip)}</span>
    </div>
    <p class="profile-lead">${escapeHtml(status.lead)}</p>
    <div class="taste-meter" aria-hidden="true"><i style="width:${progress}%"></i></div>
    <div class="stat-grid">
      <div class="stat"><b>${likes}</b><span>лайков</span></div>
      <div class="stat"><b>${skips}</b><span>скипов</span></div>
      <div class="stat"><b>${pct(p.explore_ratio)}</b><span>нового сейчас</span></div>
    </div>
    <div class="explore-bounds">
      <strong>Сколько нового в радио</strong>
      <p class="profile-detail">Знакомое — похожее на лайки. Новое — соседнее, забытое и случайное.</p>
      <label class="bound-row">
        <span class="bound-name">не меньше</span>
        <input class="slider" id="explore-lo" type="range" min="0" max="0.6" step="0.05" value="${Number(p.explore_lo ?? 0.1)}">
        <span class="bound-val" id="explore-lo-val"></span>
      </label>
      <label class="bound-row">
        <span class="bound-name">не больше</span>
        <input class="slider" id="explore-hi" type="range" min="0.1" max="0.8" step="0.05" value="${Number(p.explore_hi ?? 0.4)}">
        <span class="bound-val" id="explore-hi-val"></span>
      </label>
      <div class="profile-detail" id="explore-bounds-label"></div>
      <button type="button" class="btn" id="btn-explore-save">Сохранить</button>
    </div>
    <div class="profile-artists">
      <strong>Часто играет</strong>
      <div class="artist-row">${artists}</div>
    </div>`;
}

function tasteStatus(value) {
  return {
    discovering: {
      title: "Радио ещё знакомится",
      chip: "начало",
      lead: "Слушай и скипай — так оно запоминает, что тебе заходит.",
    },
    forming: {
      title: "Вкус уже проявляется",
      chip: "учится",
      lead: "Ещё несколько лайков — и подбор станет увереннее.",
    },
    ready: {
      title: "Радио знает твой вкус",
      chip: "готово",
      lead: "Дальше оно опирается на лайки и то, что ты дослушиваешь.",
    },
  }[value] || { title: "Твой вкус", chip: "—", lead: "" };
}

function maturityLabel(value) {
  return tasteStatus(value).title;
}

function renderMonitor(p, week, rec) {
  const outcomes = rec.outcomes || week || {};
  const overall = outcomes.overall || {};
  const explore = rec.explore || p;
  const policy = rec.last_policy || {};
  const decision = policy.policy || {};
  const sources = (outcomes.breakdowns || []).filter((row) => row.dimension === "source");
  const sourceRows = sources.length
    ? sources.map((row) => {
        const finish = Math.round((row.finish_rate || 0) * 100);
        const skip = Math.round((row.early_skip_rate || 0) * 100);
        return `<div class="source-row">
          <div class="source-head">
            <span class="tag source-${escapeHtml(row.value)}">${escapeHtml(sourceLabel(row.value))}</span>
            <span>${row.played} треков · ${finish}% дослушано</span>
          </div>
          <div class="bars" title="зелёный — дослушал, оранжевый — скипнул">
            <i class="finish" style="width:${finish}%"></i>
            <i class="skip" style="width:${skip}%"></i>
          </div>
        </div>`;
      }).join("")
    : `<p class="profile-detail">Запусти радио — здесь появится, какие треки заходят, а какие ты скипаешь.</p>`;
  const novelty = decision.explore_share != null
    ? `В последнем наборе нового было ${pct(decision.explore_share)}.`
    : "После запуска радио здесь будет видно, сколько нового оно подмешало.";
  const learning = explore.bandit_ready
    ? "Радио уже само подкручивает новизну по твоим скипах и дослушиваниям."
    : "Пока новизна в заданных тобой границах. Чем больше слушаешь, тем точнее подбор.";
  $("monitor-box").innerHTML = `
    <div class="panel-kicker">радио</div>
    <div class="monitor-title"><strong>Как играет</strong></div>
    <div class="stat-grid">
      <div class="stat"><b>${week.listens_7d || overall.played || 0}</b><span>за неделю</span></div>
      <div class="stat"><b>${pct(overall.finish_rate || (week.completes_7d && week.listens_7d ? week.completes_7d / week.listens_7d : 0))}</b><span>дослушано</span></div>
      <div class="stat"><b>${pct(week.skip_rate_7d || overall.early_skip_rate)}</b><span>скипов</span></div>
      <div class="stat"><b>${week.unique_artists_7d || 0}</b><span>артистов</span></div>
    </div>
    <div class="monitor-block">
      <strong>Откуда берутся треки</strong>
      <p class="profile-detail bar-legend"><span class="dot finish"></span> дослушал <span class="dot skip"></span> скипнул</p>
      ${sourceRows}
    </div>
    <p class="profile-lead">${novelty}</p>
    <p class="profile-detail">${learning}</p>`;
}

function paintRange(input) {
  if (!input) return;
  const min = Number(input.min);
  const max = Number(input.max);
  const span = max - min;
  const pct = span > 0 ? ((Number(input.value) - min) / span) * 100 : 0;
  input.style.setProperty("--fill", `${Math.max(0, Math.min(100, pct))}%`);
}

function bindExploreBounds() {
  const lo = $("explore-lo");
  const hi = $("explore-hi");
  const label = $("explore-bounds-label");
  const save = $("btn-explore-save");
  const loVal = $("explore-lo-val");
  const hiVal = $("explore-hi-val");
  if (!lo || !hi || !label || !save) return;
  const render = () => {
    let a = Number(lo.value);
    let b = Number(hi.value);
    if (a >= b) {
      a = Math.max(0, b - 0.05);
      lo.value = String(a);
    }
    if (loVal) loVal.textContent = pct(a);
    if (hiVal) hiVal.textContent = pct(b);
    label.textContent = `Нового будет от ${pct(a)} до ${pct(b)}`;
    paintRange(lo);
    paintRange(hi);
  };
  lo.oninput = render;
  hi.oninput = render;
  save.onclick = async () => {
    const data = await api("/api/profile/explore", {
      method: "PUT",
      body: JSON.stringify({
        explore_lo: Number(lo.value),
        explore_hi: Number(hi.value),
      }),
    });
    lo.value = data.explore_lo;
    hi.value = data.explore_hi;
    render();
    toast("Сохранено");
  };
  render();
}

async function createShareLink() {
  const data = await api("/api/share/radio", {
    method: "POST",
    body: JSON.stringify({ name: "musik radio" }),
  });
  const url = data.url || data.url_bare;
  try {
    await navigator.clipboard.writeText(url);
    toast("Ссылка скопирована");
  } catch (_) {
    toast("Ссылка создана");
  }
  const line = $("share-url-line");
  if (line) {
    line.hidden = false;
    line.innerHTML = `<a href="${escapeHtml(url)}" target="_blank" rel="noopener">${escapeHtml(url)}</a>`;
  }
  await loadShares().catch(() => {});
  return url;
}

async function loadShares() {
  const list = $("share-list");
  if (!list) return;
  const data = await api("/api/share/radio");
  const shares = data.shares || [];
  if (!shares.length) {
    list.innerHTML = `<li class="share-empty">Пока нет ссылок — нажми «Создать»</li>`;
    return;
  }
  list.innerHTML = shares
    .map((sh) => {
      const active = sh.active !== false;
      return `<li class="share-item ${active ? "" : "revoked"}">
        <div class="share-main">
          <a href="${escapeHtml(sh.url)}" target="_blank" rel="noopener">${escapeHtml(sh.url)}</a>
          <span class="shelf-hint">${active ? `слушали ${sh.listen_count || 0}` : "отозвана"}</span>
        </div>
        ${
          active
            ? `<button type="button" class="chip" data-revoke="${escapeHtml(sh.token)}">отозвать</button>
               <button type="button" class="chip" data-copy="${escapeHtml(sh.url)}">копировать</button>`
            : ""
        }
      </li>`;
    })
    .join("");
  list.querySelectorAll("[data-revoke]").forEach((btn) => {
    btn.onclick = async () => {
      try {
        await api(`/api/share/radio/${encodeURIComponent(btn.dataset.revoke)}`, {
          method: "DELETE",
        });
        toast("Ссылка отозвана");
        loadShares().catch(console.error);
      } catch (e) {
        toast(e.message || String(e));
      }
    };
  });
  list.querySelectorAll("[data-copy]").forEach((btn) => {
    btn.onclick = async () => {
      try {
        await navigator.clipboard.writeText(btn.dataset.copy);
        toast("Скопировано");
      } catch (_) {
        toast(btn.dataset.copy);
      }
    };
  });
}

function togglePlay() {
  const audio = $("audio");
  if (!audio.src) {
    toast("Сначала выбери микс или трек");
    return;
  }
  if (audio.paused) {
    audio.play().then(() => setPlayIcon(true)).catch(() => toast("Не удалось начать воспроизведение"));
  } else {
    audio.pause();
    setPlayIcon(false);
  }
}

async function skipTrack() {
  const audio = $("audio");
  const fromId = current?.id;
  bumpPlayback();
  audio.pause();
  const data = await postEvent("skip", {
    reason: "skipped",
    track_id: fromId,
    impression_id: current?.impression_id,
    listened_sec: listenedAccum,
    duration_sec: audio.duration || current?.duration || 0,
  });
  const nextId = data?.next?.id || data?.next_id;
  if (audio.paused && (!nextId || Number(nextId) === Number(fromId))) {
    audio.play().catch(() => {});
  }
}

const VOLUME_ICON = `<svg class="glyph" viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M4.5 9.2h3.4L12.2 5v14l-4.3-4.2H4.5z"/><path fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" d="M16 9.2a4.2 4.2 0 0 1 0 5.6M18.2 7a7 7 0 0 1 0 10"/></svg>`;
const MUTE_ICON = `<svg class="glyph" viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M4.5 9.2h3.4L12.2 5v14l-4.3-4.2H4.5z"/><path fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" d="M16 10l4 4m0-4-4 4"/></svg>`;
let volumeOn = 1;

function savedVolume(key, fallback) {
  const n = Number(localStorage.getItem(key));
  if (!Number.isFinite(n)) return fallback;
  return Math.max(0, Math.min(1, n));
}

function setVolume(level) {
  const audio = $("audio");
  const value = Math.max(0, Math.min(1, Number(level) || 0));
  if (audio) audio.volume = value;
  for (const id of ["volume", "mini-volume"]) {
    const input = $(id);
    if (!input) continue;
    input.value = String(value);
    paintRange(input);
  }
  const mute = $("btn-mute");
  if (mute) {
    mute.innerHTML = value <= 0.001 ? MUTE_ICON : VOLUME_ICON;
    mute.title = value <= 0.001 ? "Включить звук" : "Выключить звук";
  }
  localStorage.setItem("musik_volume", String(value));
  if (value > 0.001) {
    volumeOn = value;
    localStorage.setItem("musik_volume_on", String(value));
  }
}

function toggleMute() {
  const audio = $("audio");
  if (!audio) return;
  if (audio.volume > 0.001) setVolume(0);
  else setVolume(volumeOn > 0.001 ? volumeOn : 1);
}

async function backTrack() {
  const audio = $("audio");
  if (!audio?.src || !current) {
    toast("Сейчас ничего не играет");
    return;
  }
  if ((audio.currentTime || 0) > 3) {
    audio.currentTime = 0;
    lastPos = 0;
    listenedAccum = 0;
    const seek = $("seek");
    if (seek) seek.value = 0;
    setSeekPct(0);
    $("time-cur").textContent = "0:00";
    if (audio.paused) audio.play().then(() => setPlayIcon(true)).catch(() => {});
    return;
  }
  const data = await api("/api/session/back", {
    method: "POST",
    body: JSON.stringify({ session_id: sessionId }),
  });
  audio.dataset.trackId = "";
  applyPlayPayload(data);
}

let resumeAfterSeek = false;

function finishSeek() {
  const audio = $("audio");
  seeking = false;
  lastPos = audio.currentTime || 0;
  if (resumeAfterSeek && audio.paused && audio.src) {
    audio.play().catch(() => {});
  }
  resumeAfterSeek = false;
}

function commitSeek() {
  const audio = $("audio");
  const seek = $("seek");
  if (!seeking) return;
  if (!audio.duration || !Number.isFinite(audio.duration)) {
    finishSeek();
    return;
  }
  const t = Math.max(0, Math.min(audio.duration - 0.05, (Number(seek.value) / 1000) * audio.duration));
  if (!audio.paused) resumeAfterSeek = true;
  audio.currentTime = t;
  lastPos = t;
  setSeekPct((t / audio.duration) * 100);
  if (Math.abs((audio.currentTime || 0) - t) < 0.05 && !audio.seeking) finishSeek();
}

function wireAudio() {
  const audio = $("audio");
  const seek = $("seek");

  audio.addEventListener("timeupdate", () => {
    const pos = audio.currentTime || 0;
    if (pos > lastPos) listenedAccum += pos - lastPos;
    lastPos = pos;
    if (!seeking && !audio.seeking && audio.duration && Number.isFinite(audio.duration)) {
      const pct = (pos / audio.duration) * 100;
      seek.value = Math.round((pos / audio.duration) * 1000);
      setSeekPct(pct);
      $("time-cur").textContent = fmtTime(pos);
      $("time-dur").textContent = fmtTime(audio.duration);
    }
    const now = Date.now();
    if (now - lastProgressAt > 4000 && current) {
      lastProgressAt = now;
      postEvent("progress", {
        position_sec: pos,
        duration_sec: audio.duration || current.duration || 0,
        listened_sec: listenedAccum,
      }).catch(() => {});
    }
  });
  audio.addEventListener("loadedmetadata", () => {
    $("time-dur").textContent = fmtTime(audio.duration || 0);
  });
  audio.addEventListener("play", () => setPlayIcon(true));
  audio.addEventListener("pause", () => {
    if (seeking || audio.seeking) return;
    setPlayIcon(false);
  });
  audio.addEventListener("seeked", finishSeek);
  audio.addEventListener("ended", () => {
    const id = Number(audio.dataset.trackId || 0);
    const gen = Number(audio.dataset.gen || 0);
    if (!id || gen !== playbackGen || id !== current?.id) return;
    postEvent("track_end", {
      reason: "completed",
      track_id: id,
      impression_id: current?.impression_id,
      listened_sec: listenedAccum,
      duration_sec: audio.duration || current?.duration || 0,
    }).catch(console.error);
  });

  const startSeek = () => {
    seeking = true;
  };
  seek.addEventListener("pointerdown", startSeek);
  seek.addEventListener("mousedown", startSeek);
  seek.addEventListener("touchstart", startSeek, { passive: true });
  seek.addEventListener("pointerup", commitSeek);
  seek.addEventListener("mouseup", commitSeek);
  seek.addEventListener("touchend", commitSeek);
  seek.addEventListener("change", commitSeek);
  seek.addEventListener("input", () => {
    seeking = true;
    if (audio.duration) {
      const t = (Number(seek.value) / 1000) * audio.duration;
      $("time-cur").textContent = fmtTime(t);
      setSeekPct((Number(seek.value) / 1000) * 100);
    }
  });
  seek.addEventListener("keydown", (e) => {
    if (["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)) {
      seeking = true;
      setTimeout(commitSeek, 0);
    }
  });
}

const AUDIO_EXTS = new Set([".mp3", ".flac", ".m4a", ".wav", ".ogg", ".opus"]);
const UPLOAD_BATCH = 8;

function audioExt(name) {
  const i = String(name || "").lastIndexOf(".");
  return i < 0 ? "" : String(name).slice(i).toLowerCase();
}

function fileRelPath(file) {
  return String(file._relPath || file.webkitRelativePath || file.name || "").replaceAll("\\", "/");
}

function withRelPath(file, rel) {
  try {
    Object.defineProperty(file, "_relPath", { value: rel.replaceAll("\\", "/"), configurable: true });
  } catch (_) {}
  return file;
}

function readAllEntries(reader) {
  return new Promise((resolve, reject) => {
    const out = [];
    const tick = () => {
      reader.readEntries((entries) => {
        if (!entries.length) {
          resolve(out);
          return;
        }
        out.push(...entries);
        tick();
      }, reject);
    };
    tick();
  });
}

async function filesFromEntry(entry, prefix = "") {
  if (!entry) return [];
  if (entry.isFile) {
    const file = await new Promise((resolve, reject) => entry.file(resolve, reject));
    return [withRelPath(file, prefix + file.name)];
  }
  if (!entry.isDirectory) return [];
  const nextPrefix = prefix + entry.name + "/";
  const children = await readAllEntries(entry.createReader());
  const nested = await Promise.all(children.map((child) => filesFromEntry(child, nextPrefix)));
  return nested.flat();
}

async function filesFromDataTransfer(dt) {
  const items = [...(dt.items || [])];
  if (items.some((item) => typeof item.webkitGetAsEntry === "function" && item.webkitGetAsEntry())) {
    const groups = await Promise.all(
      items
        .filter((item) => item.kind === "file")
        .map((item) => filesFromEntry(item.webkitGetAsEntry()))
    );
    return groups.flat();
  }
  return [...(dt.files || [])];
}

function setUploadBusy(busy) {
  ["btn-upload-file", "btn-upload-files", "btn-upload-folder"].forEach((id) => {
    const btn = $(id);
    if (!btn) return;
    btn.disabled = busy;
    btn.classList.toggle("loading", busy);
  });
  $("upload-drop")?.classList.toggle("is-busy", busy);
}

function setOpBar({ state = "idle", title, detail, pct } = {}) {
  const bar = $("op-bar");
  const fill = $("op-fill");
  const titleEl = $("op-title");
  const detailEl = $("op-detail");
  const pctEl = $("op-pct");
  const track = $("op-track");
  if (!bar) return;
  const value = Math.max(0, Math.min(100, Number(pct) || 0));
  bar.dataset.state = state;
  if (titleEl && title) titleEl.textContent = title;
  if (detailEl && detail != null) detailEl.textContent = detail;
  if (pctEl) pctEl.textContent = `${Math.round(value)}%`;
  if (fill) fill.style.setProperty("--op-pct", `${value}%`);
  if (track) track.setAttribute("aria-valuenow", String(Math.round(value)));
}

function jobProgress(job) {
  const prog = job?.progress || job?.result?.progress || {};
  const pct = Number(prog.pct);
  const bits = [prog.phase, prog.message].filter(Boolean);
  return {
    pct: Number.isFinite(pct) ? pct : null,
    detail: bits.join(" · ") || "",
  };
}

const JOB_NAMES = {
  full_rescan: "Обновление библиотеки",
  scan: "Сканирование",
  embed: "Аудиоэмбеддинги",
  clusters: "Кластеры",
  daily: "Daily Mix",
  album_tips: "Подсказки альбомов",
  mix_pack: "Набор миксов",
};

const JOB_STATES = {
  pending: "В очереди",
  running: "Выполняется",
  done: "Готово",
  failed: "Ошибка",
};

function renderBackgroundJobs(jobs) {
  const list = $("jobs-list");
  const badge = $("jobs-badge");
  if (!list || !badge) return;
  const active = jobs.filter((job) => job.status === "pending" || job.status === "running");
  badge.textContent = active.length ? `${active.length} активн.` : "нет активных";
  list.replaceChildren();
  if (!jobs.length) {
    const empty = document.createElement("p");
    empty.className = "jobs-empty";
    empty.textContent = "Фоновых задач пока нет.";
    list.append(empty);
    return;
  }
  jobs.slice(0, 12).forEach((job) => {
    const progress = jobProgress(job);
    const pct = job.status === "done"
      ? 100
      : Math.max(0, Math.min(100, progress.pct ?? 0));
    const row = document.createElement("article");
    row.className = "job-row";
    row.dataset.status = job.status || "";

    const head = document.createElement("div");
    head.className = "job-row-head";
    const title = document.createElement("strong");
    title.textContent = `${JOB_NAMES[job.kind] || job.kind || "Задача"} #${job.id}`;
    const state = document.createElement("span");
    state.className = "job-state";
    state.textContent = JOB_STATES[job.status] || job.status || "—";
    head.append(title, state);

    const track = document.createElement("div");
    track.className = "job-progress";
    const fill = document.createElement("span");
    fill.style.setProperty("--job-pct", `${pct}%`);
    track.append(fill);

    const meta = document.createElement("div");
    meta.className = "job-row-meta";
    const message = document.createElement("span");
    message.className = "job-message";
    message.textContent = job.error || progress.detail || (job.status === "pending" ? "Ожидает запуска" : "—");
    const percent = document.createElement("span");
    percent.textContent = `${Math.round(pct)}%`;
    meta.append(message, percent);
    row.append(head, track, meta);
    list.append(row);
  });
}

async function refreshBackgroundJobs({ initial = false } = {}) {
  const out = await api("/api/jobs?limit=20");
  const jobs = Array.isArray(out?.jobs) ? out.jobs : [];
  if (!initial) {
    let libraryChanged = false;
    jobs.forEach((job) => {
      const previous = knownJobStatuses.get(job.id);
      if (previous && previous !== job.status && job.status === "done") {
        toast(`${JOB_NAMES[job.kind] || job.kind} завершено`);
        if (["full_rescan", "scan", "embed", "clusters"].includes(job.kind)) {
          libraryChanged = true;
        }
      } else if (previous && previous !== job.status && job.status === "failed") {
        toast(`${JOB_NAMES[job.kind] || job.kind}: ошибка`);
      }
    });
    if (libraryChanged) {
      library = [];
      homeHydrated = false;
      loadLibrary().catch(() => {});
      loadHomeCatalog().catch(() => {});
    }
  }
  jobs.forEach((job) => knownJobStatuses.set(job.id, job.status));
  renderBackgroundJobs(jobs);
  return jobs;
}

function startBackgroundJobsMonitor() {
  clearInterval(backgroundJobsTimer);
  refreshBackgroundJobs({ initial: true }).catch(console.error);
  backgroundJobsTimer = setInterval(() => {
    if (!document.hidden) refreshBackgroundJobs().catch(() => {});
  }, 3000);
}

function queueMusicUpload(fileList) {
  const files = [...fileList];
  if (!files.length) return Promise.resolve();
  const waiting = uploadQueueDepth;
  uploadQueueDepth++;
  if (waiting > 0) {
    toast(`Добавлено в очередь загрузки: ${files.length}`);
  }
  uploadQueue = uploadQueue
    .catch(() => {})
    .then(() => uploadMusicFiles(files))
    .finally(() => {
      uploadQueueDepth = Math.max(0, uploadQueueDepth - 1);
    });
  return uploadQueue;
}

async function uploadMusicFiles(fileList) {
  const all = [...fileList];
  const files = all.filter((f) => AUDIO_EXTS.has(audioExt(f.name)));
  const skippedExt = all.length - files.length;
  if (!files.length) {
    setOpBar({
      state: "error",
      title: "Нет аудиофайлов",
      detail: skippedExt
        ? `Пропущено ${skippedExt}: нужны MP3, FLAC, M4A, WAV, OGG или Opus.`
        : "Файлы не выбраны.",
      pct: 0,
    });
    return;
  }
  setUploadBusy(true);
  setOpBar({
    state: "run",
    title: "Загрузка на диск",
    detail: `0 из ${files.length}`,
    pct: 0,
  });
  let saved = 0;
  let skipped = skippedExt;
  try {
    for (let i = 0; i < files.length; i += UPLOAD_BATCH) {
      const batch = files.slice(i, i + UPLOAD_BATCH);
      const fd = new FormData();
      batch.forEach((file) => {
        fd.append("file", file);
        fd.append("path", fileRelPath(file));
      });
      const res = await fetch("/api/library/upload", {
        method: "POST",
        credentials: "same-origin",
        body: fd,
      });
      if (res.status === 401) {
        showLogin("Нужен вход");
        throw new Error("unauthorized");
      }
      if (!res.ok) {
        let msg = res.statusText;
        try {
          const j = await res.json();
          msg = j.error || msg;
        } catch (_) {}
        throw new Error(msg);
      }
      const out = await res.json();
      saved += Number(out.count || 0);
      skipped += Array.isArray(out.skipped) ? out.skipped.length : 0;
      const done = Math.min(i + batch.length, files.length);
      setOpBar({
        state: "run",
        title: "Загрузка на диск",
        detail: `${done} из ${files.length} · сохранено ${saved}${skipped ? ` · пропущено ${skipped}` : ""}`,
        pct: (done / files.length) * 55,
      });
    }
    if (!saved) {
      setOpBar({
        state: "error",
        title: "Ничего не сохранено",
        detail: skipped ? `Пропущено ${skipped} файлов.` : "Сервер не принял файлы.",
        pct: 0,
      });
      return;
    }
    setOpBar({
      state: "run",
      title: "Постановка в очередь",
      detail: `На диске ${saved} файлов. Создаём фоновую задачу…`,
      pct: 95,
    });
    const job = await api("/api/library/rescan", { method: "POST", body: "{}" });
    const id = job.id || job.job_id;
    await refreshBackgroundJobs().catch(() => {});
    setOpBar({
      state: "done",
      title: "Файлы загружены",
      detail: skipped
        ? `Добавлено ${saved}, пропущено ${skipped}. Обработка идёт в задаче #${id || "—"}.`
        : `Добавлено ${saved}. Обработка идёт в задаче #${id || "—"}.`,
      pct: 100,
    });
    toast(`Фоновая задача #${id || "—"} добавлена`);
  } catch (e) {
    setOpBar({
      state: "error",
      title: "Операция прервалась",
      detail: e.message || String(e),
      pct: 0,
    });
  } finally {
    setUploadBusy(false);
    ["upload-file", "upload-files", "upload-folder"].forEach((id) => {
      const el = $(id);
      if (el) el.value = "";
    });
  }
}

function wireUploads() {
  const bind = (btnId, inputId) => {
    const btn = $(btnId);
    const input = $(inputId);
    if (!btn || !input) return;
    btn.onclick = () => input.click();
    input.onchange = () => {
      if (input.files?.length) queueMusicUpload(input.files).catch((e) => {
        setOpBar({ state: "error", title: "Ошибка", detail: e.message || String(e), pct: 0 });
      });
    };
  };
  bind("btn-upload-file", "upload-file");
  bind("btn-upload-files", "upload-files");
  bind("btn-upload-folder", "upload-folder");

  const zone = $("upload-drop");
  const view = $("view-upload");
  if (!zone || !view) return;
  zone.onclick = () => $("upload-files")?.click();
  zone.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      $("upload-files")?.click();
    }
  });

  let dragDepth = 0;
  const highlight = (on) => zone.classList.toggle("is-drag", on);
  const onDragOver = (e) => {
    if (![...e.dataTransfer.types].includes("Files")) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
  };
  const onDragEnter = (e) => {
    if (![...e.dataTransfer.types].includes("Files")) return;
    e.preventDefault();
    dragDepth++;
    highlight(true);
  };
  const onDragLeave = () => {
    dragDepth = Math.max(0, dragDepth - 1);
    if (!dragDepth) highlight(false);
  };
  const onDrop = (e) => {
    if (![...e.dataTransfer.types].includes("Files")) return;
    e.preventDefault();
    dragDepth = 0;
    highlight(false);
    filesFromDataTransfer(e.dataTransfer)
      .then((files) => queueMusicUpload(files))
      .catch((err) => setOpBar({ state: "error", title: "Ошибка", detail: err.message || String(err), pct: 0 }));
  };
  [view].forEach((el) => {
    el.addEventListener("dragenter", onDragEnter);
    el.addEventListener("dragover", onDragOver);
    el.addEventListener("dragleave", onDragLeave);
    el.addEventListener("drop", onDrop);
  });
}

async function refreshMixes() {
  const btn = $("btn-refresh-mixes");
  const label = $("refresh-label");
  const status = $("job-status");
  btn.disabled = true;
  btn.classList.add("loading");
  label.textContent = "Собираем…";
  status.hidden = false;
  status.textContent = "Миксы генерируются в фоне";
  try {
    const job = await api("/api/jobs/mix_pack", { method: "POST", body: "{}" });
    const id = job.id || job.job_id;
    toast("Миксы поставлены в очередь");
    clearInterval(jobPollTimer);
    let tries = 0;
    jobPollTimer = setInterval(async () => {
      tries++;
      try {
        if (id) {
          const j = await api(`/api/jobs/${id}`);
          const prog = j.progress || j.result?.progress;
          if (prog?.message) {
            status.textContent = prog.message;
          } else if (prog?.pct != null) {
            status.textContent = `${prog.phase || "job"} ${prog.pct}%`;
          }
          if (j.status === "done") {
            clearInterval(jobPollTimer);
            status.textContent = "Готово";
            toast("Миксы обновлены");
            await Promise.all([loadMixes(), loadSimilarRecs()]);
            setTimeout(() => {
              status.hidden = true;
            }, 1500);
            btn.disabled = false;
            btn.classList.remove("loading");
            label.textContent = "Обновить миксы";
            return;
          }
          if (j.status === "failed") {
            clearInterval(jobPollTimer);
            status.hidden = true;
            btn.disabled = false;
            btn.classList.remove("loading");
            label.textContent = "Обновить миксы";
            toast(j.error || "Не удалось обновить миксы");
            return;
          }
        }
      } catch (e) {
        console.warn("mix job poll failed", e);
      }
      if (tries > 40) {
        clearInterval(jobPollTimer);
        await Promise.all([loadMixes(), loadSimilarRecs()]);
        status.hidden = true;
        btn.disabled = false;
        btn.classList.remove("loading");
        label.textContent = "Обновить миксы";
        toast("Проверь полки — возможно уже готово");
      }
    }, 2000);
  } catch (e) {
    status.hidden = true;
    btn.disabled = false;
    btn.classList.remove("loading");
    label.textContent = "Обновить миксы";
    toast(e.message || String(e));
  }
}

let selectedPlaylistId = null;
let playlistCache = [];
let plAddTimer = null;
let playlistPickTrackId = null;
let playlistPickTrackIds = [];

function isCustomPlaylist(pl) {
  return pl && (pl.type === "manual" || pl.type === "smart" || pl.kind === "user");
}

function playlistKindLabel(pl) {
  if (!pl) return "свой список";
  if (typeof pl === "string") {
    if (pl === "smart") return "собирается сам";
    if (pl === "generated") return "микс";
    return "свой список";
  }
  if (pl.type === "smart") return "собирается сам";
  if (isCustomPlaylist(pl)) return "свой список";
  return "микс";
}

async function addTracksToPlaylist(playlistId, trackIds) {
  const ids = [...new Set((trackIds || []).map(Number).filter(Boolean))];
  if (!ids.length) return toast("Нечего добавить");
  try {
    const data = await api(`/api/playlists/${playlistId}/tracks`, {
      method: "POST",
      body: JSON.stringify({ track_ids: ids, source: "manual" }),
    });
    const added = data.added ?? ids.length;
    const skipped = data.skipped || 0;
    if (added && skipped) toast(`Добавлено ${added}, уже были ${skipped}`);
    else if (added) toast(added === 1 ? "Добавлено в плейлист" : `Добавлено ${added} ${ruTracks(added)}`);
    else toast("Это уже есть в плейлисте");
    if (selectedPlaylistId === Number(playlistId) && $("pl-detail") && !$("pl-detail").hidden) {
      await refreshPlaylistTracks(playlistId);
    }
    loadPlaylists().catch(() => {});
  } catch (e) {
    const msg = e.message || String(e);
    toast(/duplicate/i.test(msg) ? "Эта песня уже в плейлисте" : msg);
  }
}

async function addTrackToPlaylist(playlistId, trackId) {
  return addTracksToPlaylist(playlistId, [trackId]);
}

function closePlaylistPicker() {
  playlistPickTrackId = null;
  playlistPickTrackIds = [];
  const box = $("pl-picker");
  if (box) box.hidden = true;
}

async function pickPlaylistForTracks(trackIds) {
  const ids = [...new Set((trackIds || []).map(Number).filter(Boolean))];
  if (!ids.length) return toast("Нечего добавить");
  playlistPickTrackIds = ids;
  playlistPickTrackId = ids[0];
  if (!playlistCache.length) {
    const data = await api("/api/playlists");
    playlistCache = data.playlists || [];
  }
  const box = $("pl-picker");
  const list = $("pl-picker-list");
  if (!box || !list) return;
  const manuals = playlistCache.filter((p) => isCustomPlaylist(p) && p.type !== "smart");
  list.innerHTML = manuals.length
    ? manuals
        .map(
          (p) =>
            `<button type="button" data-pick-pl="${p.id}">${escapeHtml(p.name)} · ${p.track_count || 0}</button>`
        )
        .join("")
    : '<p class="sub">Пока нет своего плейлиста — создай ниже.</p>';
  list.querySelectorAll("[data-pick-pl]").forEach((btn) => {
    btn.onclick = async () => {
      const id = Number(btn.dataset.pickPl);
      const tracks = playlistPickTrackIds.length ? playlistPickTrackIds : [playlistPickTrackId];
      closePlaylistPicker();
      await addTracksToPlaylist(id, tracks);
    };
  });
  box.hidden = false;
}

async function pickPlaylistForTrack(trackId) {
  return pickPlaylistForTracks([trackId]);
}

async function hideNow(targetType, preset) {
  if (!current?.id) return toast("Сейчас ничего не играет");
  const body = {
    target_type: targetType,
    action: "block",
    scope: preset === "session" ? "session" : "global",
    session_id: sessionId || undefined,
    preset,
    track_id: current.id,
    artist: current.artist,
    album: current.album,
  };
  const data = await api("/api/rules", { method: "POST", body: JSON.stringify(body) });
  toast(targetType === "artist" ? "Артист скрыт" : "Трек скрыт");
  if (data.playback) applyPlayPayload(data.playback);
  loadSimilarRecs().catch(() => {});
  loadMixes().catch(() => {});
  if ($("rules-box")) loadRules().catch(() => {});
}

async function loadContexts() {
  const box = $("contexts-box");
  if (!box) return;
  const data = await api("/api/contexts");
  const items = data.contexts || [];
  const kindLabel = { mood: "настроение", place: "место", activity: "занятие" };
  box.innerHTML = `<div class="panel-kicker">сейчас</div>
    <strong>Настроение</strong>
    <p class="sub">Подсказка радио: ночь, дорога, уборка. Можно включить несколько сразу.</p>
    <div class="form-row">
      <input id="ctx-name" type="text" placeholder="Ночная дорога" />
      <select id="ctx-kind">
        <option value="mood">настроение</option>
        <option value="place">место</option>
        <option value="activity">занятие</option>
      </select>
      <button type="button" class="btn" id="btn-ctx-create">Добавить</button>
    </div>
    <div id="ctx-list">${items.map((c) => `
      <div class="collection-item">
        <span>${escapeHtml(c.name)}<small> · ${escapeHtml(kindLabel[c.kind] || c.kind)}</small></span>
        <span>
          <button type="button" class="chip context-chip" data-ctx="${c.context_id}" data-on="1">в радио</button>
          <button type="button" class="chip" data-ctx="${c.context_id}" data-on="0">выкл</button>
          <button type="button" class="chip" data-archive-ctx="${c.context_id}">удалить</button>
        </span>
      </div>`).join("") || "<p class='sub'>Пока пусто — добавь, если хочешь сменить настроение</p>"}</div>`;
  $("btn-ctx-create").onclick = async () => {
    await api("/api/contexts", {
      method: "POST",
      body: JSON.stringify({
        name: $("ctx-name").value,
        kind: $("ctx-kind").value,
        influence: 1,
        learning_enabled: true,
      }),
    });
    toast("Добавлено");
    loadContexts();
  };
  box.querySelectorAll("[data-ctx]").forEach((btn) => {
    btn.onclick = async () => {
      if (!sessionId) return toast("Сначала запусти радио");
      const on = btn.dataset.on === "1";
      await api(`/api/contexts/${btn.dataset.ctx}/${on ? "activate" : "deactivate"}`, {
        method: "POST",
        body: JSON.stringify({ session_id: sessionId }),
      });
      toast(on ? "Включено в радио" : "Выключено");
    };
  });
  box.querySelectorAll("[data-archive-ctx]").forEach((btn) => {
    btn.onclick = async () => {
      await api(`/api/contexts/${btn.dataset.archiveCtx}`, { method: "DELETE" });
      loadContexts();
    };
  });
}

async function loadRules() {
  const box = $("rules-box");
  if (!box) return;
  const data = await api("/api/rules?all=1");
  const items = data.rules || [];
  const actionLabel = { block: "скрыто", downrank: "реже", cooldown: "пауза" };
  const targetLabel = { track: "трек", artist: "артист", album: "альбом", genre: "жанр", cluster: "похожее" };
  box.innerHTML = `<div class="panel-kicker">фильтр</div>
    <strong>Скрыто</strong>
    <p class="sub">Не попадает в радио, миксы, сгенерированные плейлисты и рекомендации, пока запрет не кончится.</p>
    <button type="button" class="btn" id="btn-rule-undo">Вернуть последнее</button>
    ${items.map((r) => `
      <div class="collection-item">
        <span>${escapeHtml(actionLabel[r.action] || r.action)} · ${escapeHtml(targetLabel[r.target_type] || r.target_type)} «${escapeHtml(r.target_key)}»</span>
        <button type="button" class="chip" data-archive-rule="${r.rule_id}">вернуть</button>
      </div>`).join("") || "<p class='sub'>Ничего не скрыто</p>"}`;
  $("btn-rule-undo").onclick = async () => {
    await api("/api/rules/undo", { method: "POST", body: "{}" });
    loadRules();
  };
  box.querySelectorAll("[data-archive-rule]").forEach((btn) => {
    btn.onclick = async () => {
      await api(`/api/rules/${btn.dataset.archiveRule}`, { method: "DELETE" });
      loadRules();
    };
  });
}

async function loadPlaylists() {
  const host = $("pl-list");
  if (!host) return;
  try {
    const data = await api("/api/playlists");
    playlistCache = data.playlists || [];
  } catch (e) {
    host.innerHTML = `<p class="sub">Не удалось загрузить плейлисты: ${escapeHtml(e.message || String(e))}</p>`;
    return;
  }
  if (!playlistCache.length) {
    host.innerHTML = '<p class="sub">Пока пусто. Создай сверху или нажми «Из библиотеки».</p>';
    return;
  }
  host.innerHTML = playlistCache
    .map(
      (p) => `
    <article class="pl-card" data-open-pl="${p.id}">
      <strong>${escapeHtml(p.name)}</strong>
      <div class="pl-meta">${p.track_count || 0} ${ruTracks(p.track_count || 0)} · ${playlistKindLabel(p)}</div>
      <div class="pl-card-actions">
        <button type="button" class="tiny" data-del-pl="${p.id}">убрать</button>
        <button type="button" class="pl-card-play" data-play-pl="${p.id}" title="Играть">▶</button>
      </div>
    </article>`
    )
    .join("");
  host.querySelectorAll("[data-open-pl]").forEach((card) => {
    card.onclick = (e) => {
      if (e.target.closest("[data-play-pl], [data-del-pl]")) return;
      openPlaylist(Number(card.dataset.openPl)).catch((err) => toast(err.message || String(err)));
    };
  });
  host.querySelectorAll("[data-play-pl]").forEach((b) => {
    b.onclick = (e) => {
      e.stopPropagation();
      api(`/api/playlists/${b.dataset.playPl}/play`, { method: "POST", body: "{}" })
        .then((data) => {
          applyPlayPayload(data);
          setView("player");
        })
        .catch((err) => toast(err.message || String(err)));
    };
  });
  host.querySelectorAll("[data-del-pl]").forEach((b) => {
    b.onclick = (e) => {
      e.stopPropagation();
      const id = Number(b.dataset.delPl);
      const pl = playlistCache.find((p) => p.id === id);
      deletePlaylist(id, pl?.name || "плейлист").catch((err) => toast(err.message || String(err)));
    };
  });
}

async function deletePlaylist(id, name) {
  if (!confirm(`Удалить плейлист «${name}»?`)) return;
  await api(`/api/playlists/${id}?hard=1`, { method: "DELETE" });
  if (selectedPlaylistId === Number(id)) {
    const box = $("pl-detail");
    if (box) box.hidden = true;
    selectedPlaylistId = null;
  }
  toast("Плейлист убран");
  await loadPlaylists();
}

function ruTracks(n) {
  const abs = Math.abs(n) % 100;
  const d = abs % 10;
  if (abs > 10 && abs < 20) return "песен";
  if (d === 1) return "песня";
  if (d >= 2 && d <= 4) return "песни";
  return "песен";
}

function playlistTrackHTML(tracks, canEdit) {
  if (!tracks.length) {
    return '<p class="sub">Пусто. Ниже — вся библиотека: треки, артисты и альбомы. Нажми, чтобы добавить.</p>';
  }
  return tracks
    .map(
      (t, i) => `
      <div class="pl-track">
        <span class="sub">${i + 1}</span>
        <button type="button" class="linkish" data-play-item="${t.track_id || ""}">${escapeHtml(t.artist || t.unresolved_artist || "")} — ${escapeHtml(t.title || t.unresolved_title || "не найден")}</button>
        <span class="sub">${fmtTime(t.duration || 0)}</span>
        ${canEdit && (t.item_id || t.track_id) ? `<button type="button" class="tiny" data-del-item="${escapeHtml(t.item_id || String(t.track_id))}">убрать</button>` : "<span></span>"}
      </div>`
    )
    .join("");
}

function bindPlaylistTrackRows(box, playlistId) {
  box.querySelectorAll("[data-play-item]").forEach((btn) => {
    btn.onclick = () => {
      const tid = Number(btn.dataset.playItem);
      if (!tid) return;
      playFixed({ track_id: tid, name: btn.textContent }).catch((e) => toast(e.message || String(e)));
    };
  });
  box.querySelectorAll("[data-del-item]").forEach((b) => {
    b.onclick = async () => {
      await api(`/api/playlists/${playlistId}/tracks/${encodeURIComponent(b.dataset.delItem)}`, { method: "DELETE" });
      refreshPlaylistTracks(playlistId);
    };
  });
}

async function refreshPlaylistTracks(id) {
  const pl = await api(`/api/playlists/${id}`);
  const tracks = pl.tracks || [];
  const canEdit = isCustomPlaylist(pl);
  const count = $("pl-count");
  if (count) count.textContent = `${tracks.length} ${ruTracks(tracks.length)} · ${playlistKindLabel(pl)}`;
  const host = $("pl-now-list");
  if (host) {
    host.innerHTML = playlistTrackHTML(tracks, canEdit);
    bindPlaylistTrackRows(host, id);
  }
}

function renderPlaylistBrowser(playlistId) {
  const q = ($("pl-add-q")?.value || "").trim().toLowerCase();
  const ul = $("pl-add-list");
  const grid = $("pl-add-grid");
  if (!ul || !grid) return;
  const { artists, albums } = groupCatalog(library);

  document.querySelectorAll("#pl-add-tabs .seg-btn").forEach((b) => {
    b.classList.toggle("active", b.dataset.plTab === plAddTab);
  });

  if (plAddTab === "tracks" || plAddTab === "favorites") {
    ul.hidden = false;
    grid.hidden = true;
    grid.innerHTML = "";
    ul.innerHTML = "";
    const source = plAddTab === "favorites" ? library.filter((t) => favoriteIds.has(t.id)) : library;
    const rows = sortTracks(
      source.filter((t) => !q || `${t.artist} ${t.title} ${t.album}`.toLowerCase().includes(q)),
      plAddSort
    ).slice(0, 400);
    if (!rows.length) {
      ul.innerHTML = `<li class="sub" style="padding:.8rem 0">${q ? "Ничего не нашлось" : "Пусто"}</li>`;
      return;
    }
    rows.forEach((t) => {
      const li = document.createElement("li");
      li.className = "track-row";
      li.innerHTML = `
        <button type="button" class="linkish">${escapeHtml(t.artist || "")} — ${escapeHtml(t.title || "")}</button>
        <span class="dur">${fmtTime(t.duration || 0)}</span>
        <span class="row-actions"><button type="button" class="tiny">добавить</button></span>`;
      const add = () => addTrackToPlaylist(playlistId, t.id);
      li.querySelector(".linkish").onclick = add;
      li.querySelector(".tiny").onclick = add;
      ul.appendChild(li);
    });
    return;
  }

  ul.hidden = true;
  ul.innerHTML = "";
  grid.hidden = false;
  grid.innerHTML = "";

  if (plAddTab === "artists") {
    sortArtists(
      artists.filter((a) => !q || a.artist.toLowerCase().includes(q)),
      plAddSort
    ).forEach((a) => {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "lib-tile artist";
      btn.innerHTML = `
        ${tileArtHTML(a.cover, a.artist)}
        <strong>${escapeHtml(a.artist)}</strong>
        <span>${a.tracks} ${ruTracks(a.tracks)}</span>
        <div class="mix-meta">добавить всё</div>`;
      btn.onclick = () =>
        addTracksToPlaylist(
          playlistId,
          tracksOfArtist(a.artist).map((t) => t.id)
        );
      grid.appendChild(btn);
    });
    return;
  }

  sortAlbums(
    albums.filter((al) => !q || `${al.artist} ${al.album}`.toLowerCase().includes(q)),
    plAddSort
  ).forEach((al) => {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "lib-tile";
    btn.innerHTML = `
      ${tileArtHTML(al.cover, al.album)}
      <strong>${escapeHtml(al.album)}</strong>
      <span>${escapeHtml(al.artist)}</span>
      <div class="mix-meta">добавить ${al.tracks} ${ruTracks(al.tracks)}</div>`;
    btn.onclick = () =>
      addTracksToPlaylist(
        playlistId,
        tracksOfAlbum(al.artist, al.album).map((t) => t.id)
      );
    grid.appendChild(btn);
  });
}

async function openPlaylist(id) {
  selectedPlaylistId = Number(id);
  const pl = await api(`/api/playlists/${id}`);
  const box = $("pl-detail");
  box.hidden = false;
  const tracks = pl.tracks || [];
  const canEdit = isCustomPlaylist(pl);
  box.innerHTML = `
    <div class="pl-detail-head">
      <div>
        <button type="button" class="text-link" id="btn-pl-back">← ко всем</button>
        <h2 style="margin:.25rem 0 0">${escapeHtml(pl.name)}</h2>
        <p class="sub" id="pl-count">${tracks.length} ${ruTracks(tracks.length)} · ${playlistKindLabel(pl)}</p>
      </div>
      <div class="form-row" style="margin:0">
        <button type="button" class="btn primary" id="btn-pl-play-here">Играть</button>
        <button type="button" class="btn" id="btn-pl-radio">Радио отсюда</button>
        ${canEdit ? `<button type="button" class="btn quiet" id="btn-pl-del">Убрать плейлист</button>` : ""}
      </div>
    </div>
    <div id="pl-now-list">${playlistTrackHTML(tracks, canEdit)}</div>
    ${
      canEdit
        ? `<div class="pl-browser">
      <h3>Добавить из библиотеки</h3>
      <div class="seg" id="pl-add-tabs" role="tablist">
        <button type="button" class="seg-btn active" data-pl-tab="tracks">Треки</button>
        <button type="button" class="seg-btn" data-pl-tab="artists">Артисты</button>
        <button type="button" class="seg-btn" data-pl-tab="albums">Альбомы</button>
        <button type="button" class="seg-btn" data-pl-tab="favorites">Избранное</button>
      </div>
      <div class="lib-tools">
        <input id="pl-add-q" type="search" placeholder="Поиск…" autocomplete="off" />
        <select id="pl-add-sort" aria-label="Сортировка"></select>
      </div>
      <ul id="pl-add-list" class="track-list"></ul>
      <div id="pl-add-grid" class="lib-grid" hidden></div>
      <div class="form-row">
        <button type="button" class="btn" id="btn-pl-now">Добавить то, что играет</button>
        <button type="button" class="btn" id="btn-pl-queue">Добавить очередь радио</button>
      </div>
    </div>`
        : ""
    }
    <div class="form-row">
      <button type="button" class="btn quiet" id="btn-pl-dup">Копия</button>
      <button type="button" class="btn quiet" id="btn-pl-export">Скачать</button>
    </div>`;
  box.scrollIntoView({ behavior: "smooth", block: "start" });
  $("btn-pl-back").onclick = () => {
    box.hidden = true;
    selectedPlaylistId = null;
  };
  $("btn-pl-play-here").onclick = async () => {
    const data = await api(`/api/playlists/${id}/play`, { method: "POST", body: "{}" });
    applyPlayPayload(data);
    setView("player");
  };
  $("btn-pl-radio").onclick = async () => {
    const data = await api(`/api/playlists/${id}/radio`, { method: "POST", body: "{}" });
    applyPlayPayload(data);
    setView("player");
  };
  bindPlaylistTrackRows($("pl-now-list") || box, id);
  const addQ = $("pl-add-q");
  if (addQ) {
    await ensureLibrary().catch(() => {});
    plAddSort = fillSortSelect($("pl-add-sort"), plAddTab, plAddSort);
    renderPlaylistBrowser(id);
    addQ.oninput = () => {
      clearTimeout(plAddTimer);
      plAddTimer = setTimeout(() => renderPlaylistBrowser(id), 80);
    };
    $("pl-add-sort").onchange = () => {
      plAddSort = $("pl-add-sort").value;
      renderPlaylistBrowser(id);
    };
    $("pl-add-tabs").querySelectorAll("[data-pl-tab]").forEach((btn) => {
      btn.onclick = () => {
        plAddTab = btn.dataset.plTab;
        plAddSort = fillSortSelect($("pl-add-sort"), plAddTab, plAddSort);
        addQ.placeholder =
          plAddTab === "artists" ? "Поиск артиста…" : plAddTab === "albums" ? "Поиск альбома…" : "Артист, трек, альбом…";
        renderPlaylistBrowser(id);
      };
    });
  }
  const addNow = $("btn-pl-now");
  if (addNow) {
    addNow.onclick = () => {
      if (!current?.id) return toast("Сейчас ничего не играет");
      addTrackToPlaylist(id, current.id);
    };
  }
  const addQueue = $("btn-pl-queue");
  if (addQueue) {
    addQueue.onclick = async () => {
      if (!sessionId) return toast("Сначала запусти радио");
      await api(`/api/playlists/${id}/from-queue`, { method: "POST", body: JSON.stringify({ session_id: sessionId }) });
      toast("Очередь добавлена");
      openPlaylist(id);
    };
  }
  $("btn-pl-dup").onclick = async () => {
    const copy = await api(`/api/playlists/${id}/duplicate`, { method: "POST", body: "{}" });
    toast("Сделана копия");
    await loadPlaylists();
    if (copy?.id) openPlaylist(copy.id);
  };
  $("btn-pl-export").onclick = () => {
    window.location.href = `/api/playlists/${id}/export`;
  };
  const delBtn = $("btn-pl-del");
  if (delBtn) {
    delBtn.onclick = () => deletePlaylist(id, pl.name).catch((e) => toast(e.message || String(e)));
  }
}

async function createEmptyPlaylist() {
  const name = ($("pl-name").value || "").trim();
  if (!name) return toast("Напиши название");
  const pl = await api("/api/playlists", {
    method: "POST",
    body: JSON.stringify({ name, type: "manual", kind: "user" }),
  });
  $("pl-name").value = "";
  const pending = playlistPickTrackIds.length
    ? playlistPickTrackIds.slice()
    : playlistPickTrackId
      ? [playlistPickTrackId]
      : [];
  playlistPickTrackId = null;
  playlistPickTrackIds = [];
  toast("Готово — добавь песни из библиотеки ниже");
  await loadPlaylists();
  if (pl?.id) {
    await openPlaylist(pl.id);
    if (pending.length) await addTracksToPlaylist(pl.id, pending);
  }
}

async function startFromLibrary() {
  let name = ($("pl-name").value || "").trim();
  if (!name) name = "Из библиотеки";
  const pl = await api("/api/playlists", {
    method: "POST",
    body: JSON.stringify({ name, type: "manual", kind: "user" }),
  });
  $("pl-name").value = "";
  toast("Открыл библиотеку — жми трек, артиста или альбом");
  await loadPlaylists();
  if (pl?.id) {
    plAddTab = "tracks";
    await openPlaylist(pl.id);
    $("pl-add-q")?.focus();
  }
}

const THEME_ACTIVE_KEY = "musik_theme";
const THEME_CUSTOM_KEY = "musik_custom_themes";
const THEME_TOKEN_KEYS = [
  "--bg", "--fg", "--accent", "--accent2", "--on-accent",
  "--radius", "--radius-sm", "--radius-pill",
  "--font", "--display", "--brand-font", "--mono",
];
const FONT_PRESETS = {
  ember: {
    "--font": '"Manrope", system-ui, sans-serif',
    "--display": '"Syne", "Manrope", sans-serif',
    "--brand-font": '"Syne", "Manrope", sans-serif',
    "--mono": '"DM Mono", ui-monospace, monospace',
  },
  grotesk: {
    "--font": '"Space Grotesk", "Manrope", system-ui, sans-serif',
    "--display": '"Space Grotesk", "Manrope", sans-serif',
    "--brand-font": '"Space Grotesk", "Manrope", sans-serif',
    "--mono": '"DM Mono", "Manrope", ui-monospace, monospace',
  },
  pixel: {
    "--font": '"Manrope", system-ui, sans-serif',
    "--display": '"Manrope", system-ui, sans-serif',
    "--brand-font": '"Press Start 2P", "Manrope", sans-serif',
    "--mono": '"DM Mono", ui-monospace, monospace',
  },
  system: {
    "--font": "system-ui, sans-serif",
    "--display": "system-ui, sans-serif",
    "--brand-font": "system-ui, sans-serif",
    "--mono": "ui-monospace, monospace",
  },
};
const THEME_ID_RE = /^[a-z][a-z0-9-]{0,31}$/;
let installedThemes = [
  { id: "ember", name: "Ember", blurb: "Warm", swatch: ["#100c0a", "#e07a3a", "#3d8f7a"], color: "#1a120c", installed: true },
  { id: "retro", name: "Retro", blurb: "Acid pixel", swatch: ["#080a0c", "#d6ff3f", "#ff4f9a"], color: "#080a0c", installed: true },
];

function hexLuma(hex) {
  const n = String(hex || "").replace("#", "");
  if (n.length !== 6) return 0;
  const ch = [0, 2, 4].map((i) => parseInt(n.slice(i, i + 2), 16) / 255);
  const f = (c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
  return 0.2126 * f(ch[0]) + 0.7152 * f(ch[1]) + 0.0722 * f(ch[2]);
}

function safeThemeValue(value) {
  return typeof value === "string" && value.length > 0 && value.length < 180 && !/[;{}]|url\s*\(|expression\s*\(/i.test(value);
}

function readCustomThemes() {
  try {
    const list = JSON.parse(localStorage.getItem(THEME_CUSTOM_KEY) || "[]");
    return Array.isArray(list) ? list.filter((t) => t && t.id && t.tokens) : [];
  } catch {
    return [];
  }
}

function readActiveTheme() {
  try {
    const saved = JSON.parse(localStorage.getItem(THEME_ACTIVE_KEY) || "null");
    if (saved && saved.id === "omarchy") {
      saved.id = "retro";
      localStorage.setItem(THEME_ACTIVE_KEY, JSON.stringify(saved));
    }
    return saved;
  } catch {
    return null;
  }
}

let appliedThemeId = readActiveTheme()?.id || "ember";

function radiusTokens(px) {
  const r = Math.max(0, Math.min(28, Number(px) || 0));
  return {
    "--radius": `${r}px`,
    "--radius-sm": `${Math.round(r * 0.66)}px`,
    "--radius-pill": r < 4 ? "0px" : "999px",
  };
}

function fontPresetId(tokens) {
  const brand = tokens["--brand-font"] || "";
  const font = tokens["--font"] || "";
  if (brand.includes("Press Start")) return "pixel";
  if (font.includes("Space Grotesk")) return "grotesk";
  if (font.startsWith("system-ui")) return "system";
  return "ember";
}

function fillThemeForm(theme) {
  if (!theme?.tokens || !$("theme-bg")) return;
  const tokens = theme.tokens;
  $("theme-name").value = theme.name || "";
  $("theme-bg").value = tokens["--bg"] || "#100c0a";
  $("theme-fg").value = tokens["--fg"] || "#f7f0e8";
  $("theme-accent").value = tokens["--accent"] || "#e07a3a";
  $("theme-accent2").value = tokens["--accent2"] || "#3d8f7a";
  const radius = parseInt(tokens["--radius"], 10);
  if (Number.isFinite(radius)) {
    $("theme-radius").value = String(radius);
    $("theme-radius-val").textContent = String(radius);
  }
  $("theme-font").value = fontPresetId(tokens);
}

function tokensFromForm() {
  const bg = $("theme-bg").value;
  const fg = $("theme-fg").value;
  const accent = $("theme-accent").value;
  const accent2 = $("theme-accent2").value;
  const preset = FONT_PRESETS[$("theme-font").value] || FONT_PRESETS.ember;
  return {
    "--bg": bg,
    "--fg": fg,
    "--accent": accent,
    "--accent2": accent2,
    "--on-accent": hexLuma(accent) > 0.45 ? "#090b08" : "#ffffff",
    ...radiusTokens($("theme-radius").value),
    ...preset,
  };
}

function ensureThemeStylesheet(id) {
  if (!THEME_ID_RE.test(id) || id === "ember") return;
  const href = `/themes/${id}.css?v=retro3`;
  if ([...document.querySelectorAll('link[rel="stylesheet"]')].some((link) => link.getAttribute("href") === href)) return;
  const link = document.createElement("link");
  link.rel = "stylesheet";
  link.href = href;
  document.head.appendChild(link);
}

function applyTheme(theme, { persist = true } = {}) {
  const root = document.documentElement;
  THEME_TOKEN_KEYS.forEach((key) => root.style.removeProperty(key));
  root.classList.remove("theme-light");
  root.style.colorScheme = "";
  const id = theme?.id || "ember";
  appliedThemeId = id;
  if (id === "ember") delete root.dataset.theme;
  else if (theme.installed && THEME_ID_RE.test(id)) {
    root.dataset.theme = id;
    ensureThemeStylesheet(id);
  } else {
    root.dataset.theme = "custom";
    const tokens = theme.tokens || {};
    THEME_TOKEN_KEYS.forEach((key) => {
      if (safeThemeValue(tokens[key])) root.style.setProperty(key, tokens[key]);
    });
    const scheme = hexLuma(tokens["--bg"]) > 0.55 ? "light" : "dark";
    if (scheme === "light") {
      root.classList.add("theme-light");
      root.style.colorScheme = "light";
    }
    theme = { ...theme, scheme };
  }
  const meta = document.querySelector('meta[name="theme-color"]');
  const color = theme?.color || theme?.tokens?.["--bg"] || "#1a120c";
  if (meta) meta.setAttribute("content", color);
  if (persist) {
    const stored = { id, color };
    if (!theme?.installed && id !== "ember") {
      stored.tokens = theme.tokens;
      stored.scheme = theme.scheme;
    }
    localStorage.setItem(THEME_ACTIVE_KEY, JSON.stringify(stored));
  }
  renderThemeList();
  syncRetroField();
}

let retroField = null;

function syncRetroField() {
  if (document.documentElement.dataset.theme === "retro") startRetroField();
  else stopRetroField();
}

function stopRetroField() {
  if (!retroField) return;
  cancelAnimationFrame(retroField.raf);
  retroField.canvas.remove();
  window.removeEventListener("resize", retroField.resize);
  document.removeEventListener("visibilitychange", retroField.onHide);
  window.removeEventListener("pointermove", retroField.onMove);
  retroField = null;
}

function startRetroField() {
  if (retroField || window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  const canvas = document.createElement("canvas");
  canvas.className = "retro-field";
  canvas.setAttribute("aria-hidden", "true");
  document.body.prepend(canvas);
  const ctx = canvas.getContext("2d");
  const colors = ["#d6ff3f", "#ff4f9a", "#62d9ff", "#ffb23e"];
  let dots = [];
  let w = 0;
  let h = 0;
  let glowX = 0;
  let glowY = 0;
  let aimX = 0;
  let aimY = 0;
  const resize = () => {
    w = canvas.width = window.innerWidth;
    h = canvas.height = window.innerHeight;
    aimX = glowX = w * 0.62;
    aimY = glowY = h * 0.16;
    const count = Math.min(78, Math.max(28, Math.floor(w * h / 22000)));
    dots = Array.from({ length: count }, () => {
      const s = Math.random() < 0.82 ? 2 : 4;
      return {
        x: Math.random() * w,
        y: Math.random() * h,
        vx: (Math.random() - 0.5) * (s === 2 ? 0.35 : 0.18),
        vy: (Math.random() - 0.5) * 0.22,
        s,
        a: 0.18 + Math.random() * 0.55,
        color: colors[Math.floor(Math.random() * colors.length)],
        blink: Math.random() < 0.35 ? 400 + Math.random() * 900 : 0,
      };
    });
  };
  const onMove = (event) => {
    aimX = event.clientX;
    aimY = event.clientY;
  };
  const step = (now) => {
    if (!retroField) return;
    if (document.hidden) {
      retroField.raf = requestAnimationFrame(step);
      return;
    }
    glowX += (aimX - glowX) * 0.04;
    glowY += (aimY - glowY) * 0.04;
    ctx.clearRect(0, 0, w, h);
    const glow = ctx.createRadialGradient(glowX, glowY, 0, glowX, glowY, Math.min(w, h) * 0.42);
    glow.addColorStop(0, "rgba(214,255,63,0.09)");
    glow.addColorStop(1, "rgba(214,255,63,0)");
    ctx.fillStyle = glow;
    ctx.fillRect(0, 0, w, h);
    for (const dot of dots) {
      dot.x += dot.vx;
      dot.y += dot.vy;
      if (dot.x < 0 || dot.x > w) dot.vx *= -1;
      if (dot.y < 0 || dot.y > h) dot.vy *= -1;
      const on = !dot.blink || Math.floor(now / dot.blink) % 2 === 0;
      ctx.globalAlpha = on ? dot.a : 0.04;
      ctx.fillStyle = dot.color;
      ctx.fillRect(Math.round(dot.x / 2) * 2, Math.round(dot.y / 2) * 2, dot.s, dot.s);
    }
    ctx.globalAlpha = 1;
    retroField.raf = requestAnimationFrame(step);
  };
  retroField = { canvas, raf: 0, resize, onMove, onHide: () => {} };
  window.addEventListener("resize", resize);
  window.addEventListener("pointermove", onMove);
  resize();
  retroField.raf = requestAnimationFrame(step);
}

async function loadInstalledThemes() {
  try {
    const data = await api("/api/themes");
    if (!Array.isArray(data.themes) || !data.themes.length) return;
    installedThemes = data.themes.filter((theme) => theme && THEME_ID_RE.test(theme.id)).map((theme) => ({
      id: theme.id,
      name: theme.name,
      blurb: theme.blurb,
      swatch: theme.swatch,
      color: theme.color,
      installed: true,
    }));
    installedThemes.forEach((theme) => ensureThemeStylesheet(theme.id));
    renderThemeList();
  } catch (err) {
    console.error(err);
  }
}

function renderThemeList() {
  const box = $("theme-list");
  if (!box) return;
  const activeId = appliedThemeId || "ember";
  const cards = [
    ...installedThemes.map((theme) => ({ ...theme, builtin: true })),
    ...readCustomThemes().map((theme) => ({
      ...theme,
      blurb: "Custom",
      swatch: [theme.tokens["--bg"], theme.tokens["--accent"], theme.tokens["--accent2"]],
      builtin: false,
    })),
  ];
  box.replaceChildren();
  cards.forEach((theme) => {
    const row = document.createElement("div");
    row.className = "theme-card-row";
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "theme-card" + (theme.id === activeId ? " active" : "");
    btn.setAttribute("aria-pressed", theme.id === activeId ? "true" : "false");
    const swatch = document.createElement("span");
    swatch.className = "theme-swatches";
    (theme.swatch || []).forEach((color) => {
      const bit = document.createElement("i");
      bit.style.background = safeThemeValue(color) ? color : "transparent";
      swatch.appendChild(bit);
    });
    const name = document.createElement("strong");
    name.textContent = theme.name || "Theme";
    const blurb = document.createElement("span");
    blurb.textContent = theme.blurb || "";
    btn.append(swatch, name, blurb);
    btn.onclick = () => {
      if (theme.tokens) fillThemeForm(theme);
      applyTheme(theme);
    };
    row.appendChild(btn);
    if (!theme.builtin) {
      const del = document.createElement("button");
      del.type = "button";
      del.className = "theme-delete";
      del.textContent = "×";
      del.title = "Delete theme";
      del.setAttribute("aria-label", `Delete theme ${theme.name || ""}`);
      del.onclick = () => deleteCustomTheme(theme.id);
      row.appendChild(del);
    }
    box.appendChild(row);
  });
}

function deleteCustomTheme(id) {
  const next = readCustomThemes().filter((theme) => theme.id !== id);
  localStorage.setItem(THEME_CUSTOM_KEY, JSON.stringify(next));
  if (readActiveTheme()?.id === id) applyTheme({ id: "ember" });
  else renderThemeList();
}

function saveCustomTheme(event) {
  event.preventDefault();
  const name = $("theme-name").value.trim();
  if (!name) return;
  const tokens = tokensFromForm();
  const theme = {
    id: `custom:${Date.now().toString(36)}`,
    name,
    tokens,
    color: tokens["--bg"],
  };
  const list = readCustomThemes();
  list.push(theme);
  localStorage.setItem(THEME_CUSTOM_KEY, JSON.stringify(list.slice(-12)));
  applyTheme(theme);
  toast("Theme saved");
}

function wireTheme() {
  renderThemeList();
  loadInstalledThemes();
  const radius = $("theme-radius");
  const radiusVal = $("theme-radius-val");
  if (radius && radiusVal) {
    radius.oninput = () => { radiusVal.textContent = radius.value; };
  }
  $("theme-preview")?.addEventListener("click", () => {
    const tokens = tokensFromForm();
    applyTheme({ id: "custom:preview", name: "Preview", tokens, color: tokens["--bg"] }, { persist: false });
  });
  $("theme-form")?.addEventListener("submit", saveCustomTheme);
  syncRetroField();
}

function wire() {
  wireTheme();
  document.querySelectorAll(".tab").forEach((b) => {
    b.onclick = () => setView(b.dataset.view);
  });
  document.querySelectorAll("[data-lib-tab]").forEach((b) => {
    b.onclick = () => {
      libTab = b.dataset.libTab;
      setView("library");
    };
  });
  document.querySelectorAll("#view-library .seg-btn").forEach((b) => {
    b.onclick = () => setLibTab(b.dataset.lib);
  });
  $("lib-sort").onchange = () => {
    libSort = $("lib-sort").value;
    renderLib($("lib-filter").value || "");
  };
  wireAllShelves();
  $("btn-radio").onclick = () => startRadio().catch((e) => toast(e.message || String(e)));
  $("btn-share-radio").onclick = () => createShareLink().catch((e) => toast(e.message || String(e)));
  $("btn-share-radio-profile").onclick = () => createShareLink().catch((e) => toast(e.message || String(e)));
  $("btn-refresh-mixes").onclick = () => refreshMixes();
  wireUploads();
  $("btn-play").onclick = togglePlay;
  $("mini-play").onclick = (e) => {
    e.stopPropagation();
    togglePlay();
  };
  $("btn-like").onclick = () => {
    if (!current?.id) return toast("Сейчас ничего не играет");
    toggleFavorite({ type: "track", track_id: current.id }).catch((e) =>
      toast(e.message || String(e))
    );
  };
  $("btn-fav-artist").onclick = () => {
    if (!current?.artist) return toast("Нет артиста");
    toggleFavorite({ type: "artist", artist: current.artist }, { withLike: false }).catch((e) =>
      toast(e.message || String(e))
    );
  };
  $("btn-fav-album").onclick = () => {
    if (!current?.album) return toast("Нет альбома");
    toggleFavorite(
      { type: "album", artist: current.artist, album: current.album },
      { withLike: false }
    ).catch((e) => toast(e.message || String(e)));
  };
  $("btn-similar-now").onclick = async () => {
    if (!current?.id) return toast("Сейчас ничего не играет");
    try {
      const [arts, albs, tracks] = await Promise.all([
        current.artist
          ? api(`/api/similar/artists?artist=${encodeURIComponent(current.artist)}`)
          : { artists: [] },
        current.album
          ? api(
              `/api/similar/albums?artist=${encodeURIComponent(current.artist || "")}&album=${encodeURIComponent(current.album)}`
            )
          : { albums: [] },
        api(`/api/similar/${current.id}`),
      ]);
      const ids = (Array.isArray(tracks) ? tracks : []).map((t) => t.id);
      if (ids.length) {
        await playFixed({
          track_ids: ids,
          name: `Похоже на «${current.title}»`,
        });
        toast("Похожие треки");
      } else if (arts.artists?.[0]) {
        await playFixed({ artist: arts.artists[0].artist });
      } else if (albs.albums?.[0]) {
        await playFixed({ artist: albs.albums[0].artist, album: albs.albums[0].album });
      } else toast("Мало похожего в библиотеке");
      // refresh similar shelf in background
      loadSimilarRecs().catch(() => {});
    } catch (e) {
      toast(e.message || String(e));
    }
  };
  $("btn-hide-track").onclick = () => hideNow("track", "day").catch((e) => toast(e.message || String(e)));
  $("btn-hide-artist").onclick = () => hideNow("artist", "week").catch((e) => toast(e.message || String(e)));
  $("btn-pl-create").onclick = () => createEmptyPlaylist().catch((e) => toast(e.message || String(e)));
  $("pl-name").addEventListener("keydown", (e) => {
    if (e.key === "Enter") {
      e.preventDefault();
      createEmptyPlaylist().catch((err) => toast(err.message || String(err)));
    }
  });
  $("btn-pl-from-lib").onclick = () => startFromLibrary().catch((e) => toast(e.message || String(e)));
  $("btn-pl-from-fav").onclick = () =>
    api("/api/playlists/from-favorites", { method: "POST", body: "{}" })
      .then(async (pl) => {
        toast("Скопировал любимое");
        await loadPlaylists();
        if (pl?.id) openPlaylist(pl.id);
      })
      .catch((e) => toast(e.message || String(e)));
  $("btn-pl-from-later").onclick = () =>
    api("/api/playlists/from-later", { method: "POST", body: "{}" })
      .then(async (pl) => {
        toast("Скопировал «Потом»");
        await loadPlaylists();
        if (pl?.id) openPlaylist(pl.id);
      })
      .catch((e) => toast(e.message || String(e)));
  $("btn-add-pl").onclick = () => {
    if (!current?.id) return toast("Сначала включи песню");
    pickPlaylistForTrack(current.id).catch((e) => toast(e.message || String(e)));
  };
  $("btn-pl-picker-cancel").onclick = closePlaylistPicker;
  $("btn-pl-picker-new").onclick = () => {
    const trackId = playlistPickTrackId;
    $("pl-picker").hidden = true;
    playlistPickTrackId = trackId;
    setView("collections");
    $("pl-name").focus();
  };
  $("pl-picker").addEventListener("click", (e) => {
    if (e.target.id === "pl-picker") closePlaylistPicker();
  });
  $("btn-pl-import").onclick = () => $("pl-import-file").click();
  $("pl-import-file").onchange = async (e) => {
    const file = e.target.files?.[0];
    if (!file) return;
    try {
      const text = await file.text();
      const ct = file.name.endsWith(".json") ? "application/json" : "audio/x-mpegurl";
      const res = await fetch("/api/playlists/import?name=" + encodeURIComponent(file.name), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": ct },
        body: text,
      });
      if (!res.ok) throw new Error((await res.json()).error || res.statusText);
      toast("Импортировано");
      await loadPlaylists();
    } catch (err) {
      toast(err.message || String(err));
    }
    e.target.value = "";
  };
  $("btn-dislike").onclick = () => postEvent("dislike").catch((e) => toast(e.message || String(e)));
  $("btn-skip").onclick = () => skipTrack().catch((e) => toast(e.message || String(e)));
  $("mini-skip").onclick = (e) => {
    e.stopPropagation();
    skipTrack().catch((e2) => toast(e2.message || String(e2)));
  };
  $("btn-back").onclick = () => backTrack().catch((e) => toast(e.message || String(e)));
  $("mini-back").onclick = (e) => {
    e.stopPropagation();
    backTrack().catch((e2) => toast(e2.message || String(e2)));
  };
  volumeOn = savedVolume("musik_volume_on", 1) || 1;
  setVolume(savedVolume("musik_volume", 1));
  $("volume").oninput = () => setVolume($("volume").value);
  $("mini-volume").oninput = () => setVolume($("mini-volume").value);
  $("btn-mute").onclick = toggleMute;
  $("mini-open").onclick = () => setView("player");
  $("btn-later").onclick = async () => {
    if (!current?.id) return toast("Сейчас ничего не играет");
    await api("/api/later", { method: "POST", body: JSON.stringify({ track_id: current.id }) });
    toast("Добавлено в «Потом»");
  };
  $("btn-discover-new").onclick = () => showTips("new").catch((e) => toast(e.message || String(e)));
  $("btn-discover-old").onclick = () => showTips("old").catch((e) => toast(e.message || String(e)));
  $("lib-filter").oninput = (e) => {
    clearTimeout(libTimer);
    libTimer = setTimeout(() => renderLib(e.target.value), 120);
  };

  document.addEventListener("keydown", (e) => {
    const tag = (e.target && e.target.tagName) || "";
    if (tag === "INPUT" || tag === "TEXTAREA") return;
    if (e.code === "Space") {
      e.preventDefault();
      togglePlay();
    } else if (e.key === "b" || e.key === "B") {
      backTrack().catch(() => {});
    } else if (e.key === "n" || e.key === "N") {
      skipTrack().catch(() => {});
    } else if (e.key === "l" || e.key === "L") {
      if (current?.id) toggleFavorite({ type: "track", track_id: current.id }).catch(() => {});
    }
  });
}

async function bootApp() {
  startBackgroundJobsMonitor();
  api("/api/profile")
    .then((p) => renderMaturity(p.maturity))
    .catch(console.error);
  loadMixes().catch(console.error);
  loadHomeCatalog().catch(console.error);
  if (sessionId) {
    api(`/api/now?session_id=${encodeURIComponent(sessionId)}`)
      .then((now) => {
        if (!now?.current) return;
        applyPlayPayload(now, { autoplay: false });
      })
      .catch(() => {
        sessionStorage.removeItem("musik_session");
        sessionId = null;
      });
  }
}

wireAudio();
wire();

$("login-form").onsubmit = (e) => {
  e.preventDefault();
  const pw = $("login-password").value;
  doLogin(pw).catch((err) => showLogin(err.message || "Ошибка входа"));
};
$("btn-logout").onclick = () => doLogout().catch((e) => toast(e.message || String(e)));
$("btn-logout-profile").onclick = () => doLogout().catch((e) => toast(e.message || String(e)));

ensureAuth()
  .then((ok) => {
    if (ok) return bootApp();
  })
  .catch((e) => {
    console.error(e);
    showLogin();
  });
