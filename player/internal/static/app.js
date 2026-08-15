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

let sessionId = sessionStorage.getItem("musik_session") || null;
let current = null;
let playlist = [];
let fixedMode = false;
let lastProgressAt = 0;
let listenedAccum = 0;
let lastPos = 0;
let library = [];
let libTab = "tracks";
let libTimer = null;
let seeking = false;
let toastTimer = null;
let jobPollTimer = null;
let favoriteIds = new Set();
let favoriteArtists = new Set();
let favoriteAlbums = new Set(); // "artist\0album"
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
  document.querySelectorAll(".tab").forEach((b) => b.classList.toggle("active", b.dataset.view === name));
  const el = document.getElementById("view-" + name);
  if (el) el.classList.add("active");
  if (name === "home") {
    loadMixes().catch(console.error);
    loadHomeCatalog().catch(console.error);
  }
  if (name === "library") loadLibrary().catch(console.error);
  if (name === "profile") {
    loadProfile().catch(console.error);
    loadShares().catch(console.error);
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

async function loadFavorites() {
  try {
    const data = await api("/api/favorites");
    favoriteIds = new Set((data.ids || []).map(Number));
    favoriteArtists = new Set((data.artists || []).map((a) => a.artist));
    favoriteAlbums = new Set(
      (data.albums || []).map((a) => albumKey(a.artist, a.album))
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
  if (a) a.classList.toggle("on", favoriteArtists.has(current.artist));
  if (b) b.classList.toggle("on", favoriteAlbums.has(albumKey(current.artist, current.album)));
}

async function toggleFavorite(payload, { withLike = true } = {}) {
  const body =
    typeof payload === "number" || typeof payload === "string"
      ? { type: "track", track_id: Number(payload) }
      : payload;
  if (!body.type) body.type = "track";
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
    const name = data.artist || body.artist;
    if (data.favorited) favoriteArtists.add(name);
    else favoriteArtists.delete(name);
    toast(data.favorited ? "Любимый артист" : "Артист убран");
  } else if (data.type === "album" || body.type === "album") {
    const key = albumKey(data.artist || body.artist, data.album || body.album);
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
    const artist = (t.artist || "Unknown").trim() || "Unknown";
    if (!artists.has(artist)) {
      artists.set(artist, { artist, tracks: 0, cover: t.artwork || null, sampleId: t.id });
    }
    const a = artists.get(artist);
    a.tracks += 1;
    if (!a.cover && t.artwork) a.cover = t.artwork;

    const album = (t.album || "").trim();
    if (!album) continue;
    const key = artist + "\0" + album;
    if (!albums.has(key)) {
      albums.set(key, {
        artist,
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
    artists: [...artists.values()].sort((x, y) => y.tracks - x.tracks || x.artist.localeCompare(y.artist, "ru")),
    albums: [...albums.values()].sort((x, y) => y.tracks - x.tracks || x.album.localeCompare(y.album, "ru")),
  };
}

function entityCoverHtml(cover, letter, round) {
  if (cover) {
    return `<div class="entity-art${round ? " round" : ""}" style="background-image:url('${cover}')"></div>`;
  }
  return `<div class="letter">${escapeHtml((letter || "♪").slice(0, 1).toUpperCase())}</div>`;
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

function setPlayIcon(playing) {
  const icon = playing ? "❚❚" : "▶";
  $("btn-play").textContent = icon;
  $("mini-play").textContent = icon;
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
  const art = $("mini-art");
  art.style.backgroundImage = track.artwork ? `url(${track.artwork})` : "";
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
    art.style.backgroundImage = "";
    updateMini(null);
    setPlayIcon(false);
    return;
  }
  $("title").textContent = track.title || "#" + track.id;
  $("artist").textContent = [track.artist, track.album].filter(Boolean).join(" · ");
  if (track.artwork) {
    art.style.backgroundImage = `url(${track.artwork})`;
    art.classList.add("has-art");
  } else {
    art.style.backgroundImage = "";
    art.classList.remove("has-art");
    $("art-fallback").textContent = (track.title || "♪").slice(0, 1).toUpperCase();
  }
  updateMini(track);
  const audio = $("audio");
  const url = track.stream || `/api/stream/${track.id}`;
  if (audio.dataset.trackId !== String(track.id)) {
    audio.dataset.trackId = String(track.id);
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

function renderQueue(queue) {
  const ol = $("queue");
  ol.innerHTML = "";
  const list = queue || [];
  $("playlist-label").textContent = "далее в радио";
  $("queue-count").textContent = list.length ? `${list.length}` : "";
  list.forEach((q) => {
    const li = document.createElement("li");
    const tags = [];
    if (q.explore) tags.push('<span class="tag">far</span>');
    if (q.new_boost) tags.push('<span class="tag">new</span>');
    li.innerHTML = `<strong>${escapeHtml(q.artist || "")}</strong> — ${escapeHtml(q.title || "")}${tags.join("")}<span class="why">${escapeHtml(q.explanation || "")}</span>`;
    ol.appendChild(li);
  });
}

function renderPlaylist(tracks, currentIndex) {
  const ol = $("playlist");
  ol.innerHTML = "";
  const list = tracks || [];
  $("playlist-label").textContent = "треки плейлиста";
  $("queue-count").textContent = list.length ? `${list.length}` : "";
  list.forEach((t, i) => {
    const id = t.id || t.track_id;
    const li = document.createElement("li");
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "playlist-item";
    if (t.current || i === currentIndex || (current && id === current.id)) {
      btn.classList.add("current");
    }
    btn.innerHTML = `
      <span class="pos">${i + 1}</span>
      <span>
        <strong>${escapeHtml(t.title || "#" + id)}</strong>
        <span class="meta">${escapeHtml(t.artist || "")}${t.album ? " · " + escapeHtml(t.album) : ""}</span>
      </span>
      <span class="dur">${fmtTime(t.duration || 0)}</span>`;
    btn.onclick = () => jumpTo(id, i).catch((e) => toast(e.message || String(e)));
    li.appendChild(btn);
    ol.appendChild(li);
  });
  const cur = ol.querySelector(".playlist-item.current");
  if (cur) cur.scrollIntoView({ block: "nearest", behavior: "smooth" });
}

function highlightPlaylist(trackId) {
  const ol = $("playlist");
  if (!ol || ol.hidden) return;
  ol.querySelectorAll(".playlist-item").forEach((btn) => {
    const pos = Number(btn.querySelector(".pos")?.textContent || 0) - 1;
    const t = playlist[pos];
    const id = t?.id || t?.track_id;
    btn.classList.toggle("current", id === trackId);
  });
  const cur = ol.querySelector(".playlist-item.current");
  if (cur) cur.scrollIntoView({ block: "nearest" });
}

async function postEvent(type, extra = {}) {
  if (!sessionId && type !== "track_start") {
    toast("Сначала запусти микс или трек");
    throw new Error("no session");
  }
  const audio = $("audio");
  const body = {
    type,
    track_id: current?.id,
    session_id: sessionId,
    position_sec: audio.currentTime || 0,
    duration_sec: audio.duration || current?.duration || 0,
    listened_sec: listenedAccum,
    ...extra,
  };
  const data = await api("/api/events", { method: "POST", body: JSON.stringify(body) });
  if (data.session_id) setSession(data.session_id);
  if (data.maturity) renderMaturity(data.maturity);
  if (data.name) $("mode-label").textContent = data.name;
  if (Array.isArray(data.tracks)) {
    playlist = data.tracks;
    renderPlaylist(playlist, data.index);
    $("queue").hidden = true;
    $("playlist").hidden = false;
  } else if (data.queue) {
    renderQueue(data.queue);
  }
  if (data.next) {
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

async function jumpTo(trackId, index) {
  if (!sessionId) throw new Error("no session");
  if (current && trackId === current.id) {
    togglePlay();
    return;
  }
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

function coverStyle(m) {
  if (m.cover_track_id) {
    return `<div class="cover-photo" style="background-image:url('/api/artwork/${m.cover_track_id}')"></div>`;
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
  await loadFavorites();
  const tracks = await ensureLibrary();
  const { artists, albums } = groupCatalog(tracks);
  renderEntityShelf($("home-artists"), artists.slice(0, 28), "artist");
  renderEntityShelf($("home-albums"), albums.slice(0, 28), "album");
  renderEntityShelf($("home-tracks"), tracks.slice(0, 36), "track");
  await loadHomeFavorites();
  await loadSimilarRecs().catch(console.error);
  wireAllShelves();
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
  document.querySelectorAll(".seg-btn").forEach((b) => b.classList.toggle("active", b.dataset.lib === tab));
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
    if (libTab === "favorites" && !source.length) {
      ul.innerHTML = '<li class="sub" style="padding:.8rem 0">Избранное пусто — жми ♥ в плеере</li>';
      return;
    }
    source
      .filter((t) => !qq || `${t.artist} ${t.title} ${t.album}`.toLowerCase().includes(qq))
      .slice(0, 400)
      .forEach((t) => {
        const li = document.createElement("li");
        li.className = "track-row";
        const isFav = favoriteIds.has(t.id);
        li.innerHTML = `
          <button type="button" class="linkish">${isFav ? "♥ " : ""}${escapeHtml(t.artist)} — ${escapeHtml(t.title)}</button>
          <span class="dur">${fmtTime(t.duration || 0)}</span>
          <span class="row-actions">
            <button type="button" class="tiny" data-act="track">Трек</button>
            <button type="button" class="tiny" data-act="fav">${isFav ? "Убрать ♥" : "♥"}</button>
            <button type="button" class="tiny" data-act="album">Альбом</button>
            <button type="button" class="tiny" data-act="artist">Артист</button>
            <button type="button" class="tiny" data-act="later">Потом</button>
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
    artists
      .filter((a) => !qq || a.artist.toLowerCase().includes(qq))
      .forEach((a) => {
        const btn = document.createElement("button");
        btn.type = "button";
        btn.className = "lib-tile artist";
        btn.innerHTML = `
          ${a.cover ? `<div class="tile-art" style="background-image:url('${a.cover}')"></div>` : `<div class="letter">${escapeHtml(a.artist.slice(0, 1))}</div>`}
          <strong>${escapeHtml(a.artist)}</strong>
          <span>${a.tracks} треков</span>
          <div class="mix-meta">слушать</div>`;
        btn.onclick = () =>
          playFixed({ artist: a.artist }).catch((e) => toast(e.message || String(e)));
        grid.appendChild(btn);
      });
    return;
  }

  albums
    .filter((al) => !qq || `${al.artist} ${al.album}`.toLowerCase().includes(qq))
    .forEach((al) => {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "lib-tile";
      btn.innerHTML = `
        ${al.cover ? `<div class="tile-art" style="background-image:url('${al.cover}')"></div>` : `<div class="letter">${escapeHtml(al.album.slice(0, 1))}</div>`}
        <strong>${escapeHtml(al.album)}</strong>
        <span>${escapeHtml(al.artist)}</span>
        <div class="mix-meta">${al.tracks} треков</div>`;
      btn.onclick = () =>
        playFixed({ artist: al.artist, album: al.album }).catch((e) => toast(e.message || String(e)));
      grid.appendChild(btn);
    });
}

async function loadProfile() {
  const [p, m] = await Promise.all([api("/api/profile"), api("/api/metrics/weekly")]);
  renderMaturity(p.maturity);
  const conf = Math.round((p.confidence || 0) * 100);
  $("profile-box").innerHTML = `
    <div><strong>${escapeHtml(p.maturity)}</strong> · уверенность ${conf}%</div>
    <div style="margin-top:.45rem;color:var(--muted)">${p.n_positive}/${p.ready_at} позитивных сигналов · explore ${Number(p.explore_ratio).toFixed(2)}</div>
    <div style="margin-top:.85rem"><strong>Топ артисты</strong><br>${
      (p.top_artists || []).map((a) => `${escapeHtml(a.artist)} · ${a.count}`).join("<br>") || "—"
    }</div>`;
  $("metrics-box").innerHTML = `
    <strong>Неделя</strong><br>
    ${m.listens_7d} прослушиваний · ${m.skips_7d} скипов (${Math.round(m.skip_rate_7d * 100)}%) · ${m.unique_artists_7d} артистов`;
  loadShares().catch(console.error);
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
  await postEvent("skip", {
    reason: "skipped",
    listened_sec: listenedAccum,
    duration_sec: $("audio").duration || current?.duration || 0,
  });
}

function commitSeek() {
  const audio = $("audio");
  const seek = $("seek");
  if (audio.duration) {
    audio.currentTime = (Number(seek.value) / 1000) * audio.duration;
    lastPos = audio.currentTime;
    setSeekPct((Number(seek.value) / 1000) * 100);
  }
  seeking = false;
}

function wireAudio() {
  const audio = $("audio");
  const seek = $("seek");

  audio.addEventListener("timeupdate", () => {
    const pos = audio.currentTime || 0;
    if (pos > lastPos) listenedAccum += pos - lastPos;
    lastPos = pos;
    if (!seeking && audio.duration) {
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
  audio.addEventListener("pause", () => setPlayIcon(false));
  audio.addEventListener("ended", () => {
    postEvent("track_end", {
      reason: "completed",
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

function wire() {
  document.querySelectorAll(".tab").forEach((b) => {
    b.onclick = () => setView(b.dataset.view);
  });
  document.querySelectorAll("[data-lib-tab]").forEach((b) => {
    b.onclick = () => {
      libTab = b.dataset.libTab;
      setView("library");
    };
  });
  document.querySelectorAll(".seg-btn").forEach((b) => {
    b.onclick = () => setLibTab(b.dataset.lib);
  });
  wireAllShelves();
  $("btn-radio").onclick = () => startRadio().catch((e) => toast(e.message || String(e)));
  $("btn-share-radio").onclick = () => createShareLink().catch((e) => toast(e.message || String(e)));
  $("btn-share-radio-profile").onclick = () => createShareLink().catch((e) => toast(e.message || String(e)));
  $("btn-refresh-mixes").onclick = () => refreshMixes();
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
  $("btn-dislike").onclick = () => postEvent("dislike").catch((e) => toast(e.message || String(e)));
  $("btn-skip").onclick = () => skipTrack().catch((e) => toast(e.message || String(e)));
  $("mini-skip").onclick = (e) => {
    e.stopPropagation();
    skipTrack().catch((e2) => toast(e2.message || String(e2)));
  };
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
    } else if (e.key === "n" || e.key === "N") {
      skipTrack().catch(() => {});
    } else if (e.key === "l" || e.key === "L") {
      if (current?.id) toggleFavorite({ type: "track", track_id: current.id }).catch(() => {});
    }
  });
}

async function bootApp() {
  try {
    const p = await api("/api/profile");
    renderMaturity(p.maturity);
  } catch (e) {
    console.error(e);
  }
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
