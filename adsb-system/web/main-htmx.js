/**
 * ADSB Map Client - Real-time Aircraft Tracking with Leaflet
 * 
 * This module provides the frontend for the ADSB tracking system.
 * It integrates a real-time aircraft table (updated via polling) with a 
 * Leaflet map showing aircraft positions, flight paths, and metadata.
 * 
 * Features:
 * - Real-time aircraft table with sortable columns (Alt, Speed, Heading, etc.)
 * - Interactive Leaflet map with aircraft markers and polyline trails
 * - Altitude-based color coding (blue low → red high)
 * - Bidirectional selection (click row → highlight on map, click marker → highlight in table)
 * - Flight path visualization with track history
 * - Persistent sorting across polling updates
 * 
 * Architecture:
 * - Server polls /api/aircraft-rows every 1 second with current sort parameters
 * - HTMX afterSettle event triggers map update when table rows change
 * - Aircraft data stored in browser Map<icao, {polyline, marker, coords[]}>
 * - Leaflet handles map rendering and interaction
 */

// Global state
let map;                      // Leaflet map instance
let aircraft;                 // Map<icao, {polyline, marker, coords[]}>
let aircraftOffline;          // Map<icao, ...> for offline/historical data
let selectedAircraft = null;  // Currently selected aircraft ICAO
let currentSortKey = null;    // Current sort column (e.g., "alt", "spd")
let currentSortAsc = true;    // Sort direction: true = ascending (▲), false = descending (▼)
let pathsVisible = true;      // Track visibility toggle (Paths button)
let activeDataTab = 'online'; // 'online' | 'offline'
// Safety limits for offline/history rendering (prevents browser freezes)
const OFFLINE_MAX_POINTS_PER_AIRCRAFT = 1500;
const OFFLINE_MAX_TOTAL_POINTS = 20000;
// Follow mode: keep map centered on selected aircraft
const FOLLOW_STORAGE_KEY = 'adsb.followSelected';
let followSelected = false;

function loadFollowFromStorage() {
  try {
    const raw = localStorage.getItem(FOLLOW_STORAGE_KEY);
    if (raw === null || raw === undefined) return;
    followSelected = raw === 'true';
  } catch (_) {}
}

function saveFollowToStorage() {
  try {
    localStorage.setItem(FOLLOW_STORAGE_KEY, followSelected ? 'true' : 'false');
  } catch (_) {}
}

function updateFollowButtonUi() {
  const btn = document.getElementById('adsb-follow');
  if (!btn) return;
  btn.textContent = followSelected ? 'Follow: On' : 'Follow: Off';
  btn.style.opacity = followSelected ? '1' : '0.75';
}

function followSelectedNow() {
  if (!followSelected || !map || !selectedAircraft) return;
  const store = (activeDataTab === 'offline') ? aircraftOffline : aircraft;
  if (!store) return;
  const entry = store.get(selectedAircraft);
  if (!entry || !entry.marker) return;
  try {
    map.panTo(entry.marker.getLatLng(), { animate: true });
  } catch (_) {}
}

// Settings
const SETTINGS_STORAGE_KEY = 'adsb.settings';
let maxTrackPoints = 200; // Max track points per aircraft (configurable)

function clamp(n, min, max) {
  return Math.max(min, Math.min(max, n));
}

function loadSettingsFromStorage() {
  try {
    const raw = localStorage.getItem(SETTINGS_STORAGE_KEY);
    if (!raw) return;
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object') return;
    if (typeof parsed.maxTrackPoints === 'number') {
      maxTrackPoints = clamp(Math.round(parsed.maxTrackPoints), 10, 1000);
    }
  } catch (_) {}
}

function saveSettingsToStorage() {
  try {
    localStorage.setItem(SETTINGS_STORAGE_KEY, JSON.stringify({ maxTrackPoints }));
  } catch (_) {}
}

function rebuildPolylinesForAllAircraft() {
  if (!map || !aircraft) return;
  for (const entry of aircraft.values()) {
    if (!entry || !entry.polylineGroup) continue;
    entry.polylineGroup.clearLayers();
    const coords = entry.coords || [];
    for (let i = 0; i < coords.length - 1; i++) {
      const c1 = coords[i];
      const c2 = coords[i + 1];
      const segColor = colorByAltitude(c1.alt || 0);
      const segment = L.polyline([[c1.lat, c1.lon], [c2.lat, c2.lon]], {
        color: segColor,
        weight: 3,
        opacity: 0.85
      });
      entry.polylineGroup.addLayer(segment);
    }
  }
}

function applyTrackLengthLimitToAllAircraft() {
  if (!aircraft) return;
  for (const entry of aircraft.values()) {
    if (!entry || !Array.isArray(entry.coords)) continue;
    while (entry.coords.length > maxTrackPoints) entry.coords.shift();
  }
}

function initializeSettingsControls() {
  loadSettingsFromStorage();

  const input = document.getElementById('adsb-setting-tracklen');
  if (input) {
    input.value = String(maxTrackPoints);
    input.addEventListener('change', () => {
      const next = parseInt(input.value, 10);
      maxTrackPoints = clamp(isNaN(next) ? maxTrackPoints : next, 10, 1000);
      input.value = String(maxTrackPoints);
      saveSettingsToStorage();
      applyTrackLengthLimitToAllAircraft();
      rebuildPolylinesForAllAircraft();
    });
  }
}

// Map styles (tile/layout switching)
const MAPSTYLE_STORAGE_KEY = 'adsb.mapStyle';
let currentMapStyle = 'dark';
let tileDark;
let tileLight;
let seaOverlay;

function loadMapStyleFromStorage() {
  try {
    const raw = localStorage.getItem(MAPSTYLE_STORAGE_KEY);
    if (raw === 'dark' || raw === 'light' || raw === 'sea') currentMapStyle = raw;
  } catch (_) {}
}

function saveMapStyleToStorage() {
  try { localStorage.setItem(MAPSTYLE_STORAGE_KEY, currentMapStyle); } catch (_) {}
}

function setMapStyle(style) {
  if (!map) return;
  const s = (style || '').toString().trim().toLowerCase();
  if (s !== 'dark' && s !== 'light' && s !== 'sea') return;
  currentMapStyle = s;
  saveMapStyleToStorage();

  // Remove existing base layers
  if (tileDark && map.hasLayer(tileDark)) map.removeLayer(tileDark);
  if (tileLight && map.hasLayer(tileLight)) map.removeLayer(tileLight);

  // Remove sea overlay
  if (seaOverlay && map.hasLayer(seaOverlay)) map.removeLayer(seaOverlay);

  if (currentMapStyle === 'dark') {
    if (tileDark && !map.hasLayer(tileDark)) tileDark.addTo(map);
  } else if (currentMapStyle === 'light') {
    if (tileLight && !map.hasLayer(tileLight)) tileLight.addTo(map);
  } else if (currentMapStyle === 'sea') {
    if (tileLight && !map.hasLayer(tileLight)) tileLight.addTo(map);
    if (seaOverlay && !map.hasLayer(seaOverlay)) seaOverlay.addTo(map);
  }
}

function initializeMapStyleControls() {
  loadMapStyleFromStorage();

  const dark = document.getElementById('adsb-mapstyle-dark');
  const light = document.getElementById('adsb-mapstyle-light');
  const sea = document.getElementById('adsb-mapstyle-sea');

  if (dark) dark.checked = currentMapStyle === 'dark';
  if (light) light.checked = currentMapStyle === 'light';
  if (sea) sea.checked = currentMapStyle === 'sea';

  function onChange() {
    if (dark?.checked) setMapStyle('dark');
    else if (light?.checked) setMapStyle('light');
    else if (sea?.checked) setMapStyle('sea');
  }

  ;[dark, light, sea].forEach(r => {
    if (!r) return;
    r.addEventListener('change', onChange);
  });
}

// Layer state: controls which data sources are visible in table + map
// v3: Online/Offline × (Simulator/Internet)
const LAYER_STORAGE_KEY = 'adsb.layers.v3';
let layerState = {
  online: { sim: true, internet: true },
  offline: { sim: true, internet: true },
};

function layerKeyForSource(source) {
  const s = (source || '').toString().trim().toLowerCase();
  if (s === '' || s === 'unknown') return 'sim';
  if (s === 'sim' || s === 'simulator') return 'sim';
  if (s === 'net' || s === 'internet' || s === 'api' || s === 'online') return 'internet';
  // Antenna / local receiver sources should be treated like "real" data
  if (s === 'dump1090' || s === 'readsb' || s === 'antenna' || s === 'rtl' || s === 'rtlsdr') return 'internet';
  // Cleanup mode: everything else counts as Simulator
  return 'sim';
}

function isLayerEnabledForSource(source, mode) {
  const key = layerKeyForSource(source);
  const m = (mode === 'offline') ? 'offline' : 'online';
  return layerState[m] && layerState[m][key] !== false;
}

function loadLayerStateFromStorage() {
  try {
    const raw = localStorage.getItem(LAYER_STORAGE_KEY);
    if (!raw) return;
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object') return;

    // Migration: older versions stored {sim, internet}
    if (parsed.sim !== undefined || parsed.internet !== undefined) {
      layerState.online.sim = parsed.sim !== undefined ? !!parsed.sim : layerState.online.sim;
      layerState.online.internet = parsed.internet !== undefined ? !!parsed.internet : layerState.online.internet;
      return;
    }

    if (parsed.online && typeof parsed.online === 'object') {
      layerState.online.sim = parsed.online.sim !== undefined ? !!parsed.online.sim : layerState.online.sim;
      layerState.online.internet = parsed.online.internet !== undefined ? !!parsed.online.internet : layerState.online.internet;
    }
    if (parsed.offline && typeof parsed.offline === 'object') {
      layerState.offline.sim = parsed.offline.sim !== undefined ? !!parsed.offline.sim : layerState.offline.sim;
      layerState.offline.internet = parsed.offline.internet !== undefined ? !!parsed.offline.internet : layerState.offline.internet;
    }
  } catch (_) {}
}

function saveLayerStateToStorage() {
  try {
    localStorage.setItem(LAYER_STORAGE_KEY, JSON.stringify(layerState));
  } catch (_) {}
}

function applyLayerVisibilityToMap() {
  if (!map || !aircraft) return;

  const applyForStore = (store, mode) => {
    if (!store) return;
    for (const entry of store.values()) {
      const visible = isLayerEnabledForSource(entry.source, mode);

      if (entry.marker) {
        const has = map.hasLayer(entry.marker);
        if (visible && !has) entry.marker.addTo(map);
        if (!visible && has) map.removeLayer(entry.marker);
      }

      if (entry.polylineGroup) {
        const shouldShow = visible && pathsVisible;
        const has = map.hasLayer(entry.polylineGroup);
        if (shouldShow && !has) entry.polylineGroup.addTo(map);
        if (!shouldShow && has) map.removeLayer(entry.polylineGroup);
      }
    }
  };

  applyForStore(aircraft, 'online');
  applyForStore(aircraftOffline, 'offline');
}

function initializeLayerControls() {
  loadLayerStateFromStorage();

  const onlineSim = document.getElementById('adsb-layer-online-sim');
  const onlineNet = document.getElementById('adsb-layer-online-internet');
  const offlineSim = document.getElementById('adsb-layer-offline-sim');
  const offlineNet = document.getElementById('adsb-layer-offline-internet');

  if (onlineSim) onlineSim.checked = !!layerState.online.sim;
  if (onlineNet) onlineNet.checked = !!layerState.online.internet;
  if (offlineSim) offlineSim.checked = !!layerState.offline.sim;
  if (offlineNet) offlineNet.checked = !!layerState.offline.internet;

  function onChange() {
    layerState.online.sim = !!onlineSim?.checked;
    layerState.online.internet = !!onlineNet?.checked;
    layerState.offline.sim = !!offlineSim?.checked;
    layerState.offline.internet = !!offlineNet?.checked;
    saveLayerStateToStorage();
    applyTableFilter();
    applyLayerVisibilityToMap();
  }

  [onlineSim, onlineNet, offlineSim, offlineNet].forEach(cb => {
    if (!cb) return;
    cb.addEventListener('change', onChange);
  });

  // Apply once on init
  applyTableFilter();
  applyLayerVisibilityToMap();
}

function applyTableFilter() {
  const input = document.getElementById('adsb-table-search');
  const tbody = activeDataTab === 'offline'
    ? document.getElementById('adsb-offline-body')
    : document.getElementById('adsb-debug-body');
  if (!tbody) return;

  const query = (input?.value || '').toString().trim().toLowerCase();
  const rows = tbody.querySelectorAll('tr');
  const mode = activeDataTab;

  rows.forEach(row => {
    const haystack = (row.textContent || '').toString().toLowerCase();
    const source = row.dataset.source || 'sim';
    const layerOk = isLayerEnabledForSource(source, mode);
    const match = (query === '' || haystack.includes(query)) && layerOk;
    row.style.display = match ? '' : 'none';
  });
}

function setActiveTab(mode) {
  activeDataTab = (mode === 'offline') ? 'offline' : 'online';

  const onlineBtn = document.getElementById('adsb-tab-online');
  const offlineBtn = document.getElementById('adsb-tab-offline');
  const onlinePanel = document.getElementById('adsb-online-panel');
  const offlinePanel = document.getElementById('adsb-offline-panel');

  if (onlinePanel) onlinePanel.style.display = (activeDataTab === 'online') ? '' : 'none';
  if (offlinePanel) offlinePanel.style.display = (activeDataTab === 'offline') ? '' : 'none';

  if (onlineBtn && offlineBtn) {
    if (activeDataTab === 'online') {
      onlineBtn.style.background = '#1e7ec8';
      onlineBtn.style.color = '#fff';
      offlineBtn.style.background = '#0f1019';
      offlineBtn.style.color = '#b7d9ff';
    } else {
      offlineBtn.style.background = '#1e7ec8';
      offlineBtn.style.color = '#fff';
      onlineBtn.style.background = '#0f1019';
      onlineBtn.style.color = '#b7d9ff';
    }
  }

  applyTableFilter();
}

function initializeTabs() {
  const onlineBtn = document.getElementById('adsb-tab-online');
  const offlineBtn = document.getElementById('adsb-tab-offline');

  if (onlineBtn) {
    onlineBtn.addEventListener('click', (e) => {
      e.preventDefault();
      setActiveTab('online');
    });
  }
  if (offlineBtn) {
    offlineBtn.addEventListener('click', (e) => {
      e.preventDefault();
      setActiveTab('offline');
    });
  }

  setActiveTab(activeDataTab);
}

function initializeTableSearch() {
  const input = document.getElementById('adsb-table-search');
  if (!input) return;

  input.addEventListener('input', () => {
    applyTableFilter();
  });
}

console.log('[ADSB] Script loaded, waiting for DOM...');

// Initialize map and UI after DOM is ready
document.addEventListener('DOMContentLoaded', function() {
  console.log('[ADSB] DOM ready, initializing map...');
  
  const center = [50, 8];      // Central Europe
  const zoom = 6;

  // Initialize Leaflet map
  map = L.map('map').setView(center, zoom);

  // Define tile layers (styles)
  const cartoAttr = '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors &copy; <a href="https://carto.com/attributions">CARTO</a>';
  tileDark = L.tileLayer('https://{s}.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}{r}.png', {
    maxZoom: 19,
    subdomains: 'abcd',
    attribution: cartoAttr
  });
  tileLight = L.tileLayer('https://{s}.basemaps.cartocdn.com/light_all/{z}/{x}/{y}{r}.png', {
    maxZoom: 19,
    subdomains: 'abcd',
    attribution: cartoAttr
  });
  seaOverlay = L.tileLayer('https://tiles.openseamap.org/seamark/{z}/{x}/{y}.png', {
    maxZoom: 19,
    attribution: '&copy; OpenSeaMap contributors'
  });

  // Apply saved style and wire controls
  initializeMapStyleControls();
  setMapStyle(currentMapStyle);

  // Initialize aircraft tracking store: ICAO -> {polyline, marker, coords[]}
  aircraft = new Map();
  aircraftOffline = new Map();
  
  // Initialize table sorting UI
  initializeTableSorting();

  // Initialize client-side table search
  initializeTableSearch();

  // Initialize tabs (Online/Offline)
  initializeTabs();

  // Initialize layer toggles (filter table + map)
  initializeLayerControls();

  // Initialize settings
  initializeSettingsControls();
  // Follow toggle state
  loadFollowFromStorage();
  updateFollowButtonUi();

  // Initialize overlay buttons (clear tracks / toggle paths)
  initializeButtons();
  
  console.log('[ADSB] Map initialized, ready for HTMX events');
});

function setPathsVisible(visible) {
  pathsVisible = !!visible;
  if (!map || !aircraft) return;

  const applyForStore = (store, mode) => {
    if (!store) return;
    for (const entry of store.values()) {
      if (!entry.polylineGroup) continue;
      const layerVisible = isLayerEnabledForSource(entry.source, mode);
      const shouldShow = pathsVisible && layerVisible;
      if (shouldShow) {
        if (!map.hasLayer(entry.polylineGroup)) entry.polylineGroup.addTo(map);
      } else {
        if (map.hasLayer(entry.polylineGroup)) map.removeLayer(entry.polylineGroup);
      }
    }
  };

  applyForStore(aircraft, 'online');
  applyForStore(aircraftOffline, 'offline');
}

function togglePathsVisible() {
  setPathsVisible(!pathsVisible);
}

function clearTracks() {
  if (!map || !aircraft) return;

  const clearForStore = (store) => {
    if (!store) return;
    for (const entry of store.values()) {
      entry.coords = [];
      if (entry.polylineGroup) {
        entry.polylineGroup.clearLayers();
        if (map.hasLayer(entry.polylineGroup)) map.removeLayer(entry.polylineGroup);
        entry.polylineGroup = null;
      }
      if (entry.marker && map.hasLayer(entry.marker)) {
        map.removeLayer(entry.marker);
      }
      entry.marker = null;
      entry.polyline = null;
    }
  };

  clearForStore(aircraft);
  clearForStore(aircraftOffline);
}

/**
 * Altitude-based color gradient: blue (low) → cyan → green → yellow → red (high)
 * @param {number} alt - Altitude in feet
 * @returns {string} RGB color string
 */
function colorByAltitude(alt) {
  const maxAlt = 45000;
  const clipped = Math.min(Math.max(alt, 0), maxAlt);
  const ratio = clipped / maxAlt;
  let r, g, b;
  if (ratio < 0.25) {
    const t = ratio / 0.25;
    r = 0;
    g = Math.round(255 * t);
    b = 255;
  } else if (ratio < 0.5) {
    const t = (ratio - 0.25) / 0.25;
    r = 0;
    g = 255;
    b = Math.round(255 * (1 - t));
  } else if (ratio < 0.75) {
    const t = (ratio - 0.5) / 0.25;
    r = Math.round(255 * t);
    g = 255;
    b = 0;
  } else {
    const t = (ratio - 0.75) / 0.25;
    r = 255;
    g = Math.round(255 * (1 - t));
    b = 0;
  }
  return `rgb(${r},${g},${b})`;
}

/**
 * Generate consistent hue-based color for ICAO identifier
 * @param {string} id - ICAO identifier
 * @returns {string} HSL color string
 */
function colorForId(id) {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h << 5) - h + id.charCodeAt(i);
  const hue = Math.abs(h) % 360;
  return `hsl(${hue},70%,45%)`;
}

// Handle HTMX swaps: when the table tbody is updated, process new aircraft data
document.addEventListener('htmx:afterSettle', function(event) {
  console.log('[HTMX] afterSettle event fired! Target:', event.detail.target.id);
  
  if (event.detail.target.id === 'adsb-debug-body') {
    const tbody = document.getElementById('adsb-debug-body');
    
    if (!map || !aircraft) {
      console.error('[HTMX] Map or aircraft not initialized yet!');
      return;
    }
    
    const rows = tbody.querySelectorAll('tr');
    console.log('[HTMX] Processing', rows.length, 'rows');
    
    rows.forEach(row => {
      // Use data attributes for coordinates
      const lat = parseFloat(row.dataset.lat);
      const lon = parseFloat(row.dataset.lon);
      const source = row.dataset.source || 'sim';

      const seen = row.dataset.seen || '';
      const dsCallsign = row.dataset.callsign || '';
      const dsSquawk = row.dataset.squawk || '';
      const dsRssi = row.dataset.rssi || '';
      const dsHeading = row.dataset.heading || '';
      const dsTrack = row.dataset.track || '';
      const dsVerticalRate = row.dataset.verticalRate || '';
      const dsMessages = row.dataset.messages || '';
      const dsOnGround = row.dataset.onGround || '';
      const dsOrigin = row.dataset.origin || '';
      const dsGeoAlt = row.dataset.geoAltFt || '';
      const dsBaroAlt = row.dataset.baroAltFt || '';
      const dsVelocity = row.dataset.velocityMs || '';
      
      // Read cells: ICAO, Callsign, Alt, Spd, Hdg, SQK, RSSI, Pred
      const cells = row.querySelectorAll('td');
      if (cells.length >= 8 && !isNaN(lat) && !isNaN(lon)) {
        const icao = cells[0].textContent.trim();
        const callsign = cells[1].textContent.trim();
        const alt = parseInt(cells[2].textContent);
        const speed = parseInt(cells[3].textContent);
        const heading = parseInt(cells[4].textContent);
        const squawk = (cells[5].textContent || '').trim();
        const rssi = parseFloat(cells[6].textContent);

        const headingFromDataset = parseInt(dsHeading);
        const track = parseInt(dsTrack);
        const verticalRate = parseInt(dsVerticalRate);
        const messages = parseInt(dsMessages);
        const onGround = (dsOnGround === 'true' || dsOnGround === '1');
        const geoAlt = parseInt(dsGeoAlt);
        const baroAlt = parseInt(dsBaroAlt);
        const velocity = parseFloat(dsVelocity);
        
        console.log('[HTMX] Processing aircraft:', icao, callsign, 'at', lat, lon, 'alt', alt);
        updateAircraft({
          icao, lat, lon, alt, speed, heading,
          callsign: dsCallsign || callsign,
          squawk: dsSquawk || squawk,
          rssi: isNaN(rssi) ? undefined : rssi,
          source,
          seen,
          track: isNaN(track) ? undefined : track,
          verticalRate: isNaN(verticalRate) ? undefined : verticalRate,
          messages: isNaN(messages) ? undefined : messages,
          onGround,
          origin: dsOrigin || undefined,
          geoAltFt: isNaN(geoAlt) ? undefined : geoAlt,
          baroAltFt: isNaN(baroAlt) ? undefined : baroAlt,
          velocityMs: isNaN(velocity) ? undefined : velocity,

          // dataset heading is authoritative if present
          heading: isNaN(headingFromDataset) ? heading : headingFromDataset,
        });
      }
    });
    
    console.log('[HTMX] Done processing. Total aircraft on map:', aircraft.size);
    
    // Attach delegated click handler once (avoid duplicating listeners on refresh)
    tbody.removeEventListener('click', handleTableBodyClick);
    tbody.addEventListener('click', handleTableBodyClick);

    // Double-click: select + enable follow
    tbody.removeEventListener('dblclick', handleTableBodyDblClick);
    tbody.addEventListener('dblclick', handleTableBodyDblClick);
    
    // Reapply selection highlighting
    if (selectedAircraft) {
      const selectedRow = document.getElementById(`aircraft-${selectedAircraft}`);
      if (selectedRow) {
        selectedRow.classList.add('selected');
        console.log('[HTMX] Selection restored for:', selectedAircraft);
      }
    }

    // Reapply search filter after any table update
    applyTableFilter();

    // Enforce current layer visibility after updates
    applyLayerVisibilityToMap();
  }
});

// Update aircraft on map
/**
 * Update aircraft position on map and in data store.
 * Creates or updates marker, polyline, and track history.
 * @param {object} data - Aircraft data {icao, lat, lon, alt, speed, heading, callsign}
 */
function updateAircraft(data) {
  const id = data.icao;
  const lat = data.lat;
  const lon = data.lon;
  const altRaw = data.alt;
  const speedRaw = data.speed;
  const altValue = (typeof altRaw === 'number' && Number.isFinite(altRaw)) ? altRaw : 0;
  const speedValue = (typeof speedRaw === 'number' && Number.isFinite(speedRaw)) ? speedRaw : 0;
  const source = data.source || 'sim';
  const sourceKey = layerKeyForSource(source);
  const isSimSource = sourceKey === 'sim';

  if (!aircraft.has(id)) {
    aircraft.set(id, {
      coords: [],          // Track history [{lat, lon, alt, speed, heading}]
      marker: null,        // Leaflet marker for current position
      polyline: null,      // Leaflet polyline for track history
      polylineGroup: null, // FeatureGroup for multi-color polyline segments
      last: null,          // Last known position
      alt: 0,
      speed: 0,
      source: source
    });
  }

  const entry = aircraft.get(id);
  entry.source = source;
  const layerVisible = isLayerEnabledForSource(source, 'online');

  entry.last = {
    icao: id,
    callsign: data.callsign,
    lat,
    lon,
    alt: (typeof altRaw === 'number' && Number.isFinite(altRaw)) ? altRaw : null,
    speed: (typeof speedRaw === 'number' && Number.isFinite(speedRaw)) ? speedRaw : null,
    heading: data.heading || 0,
    track: data.track,
    squawk: data.squawk,
    rssi: data.rssi,
    verticalRate: data.verticalRate,
    messages: data.messages,
    onGround: data.onGround,
    source,
    seen: data.seen,
    origin: data.origin,
    geoAltFt: data.geoAltFt,
    baroAltFt: data.baroAltFt,
    velocityMs: data.velocityMs,
  };

  // Follow mode: keep camera centered on the selected aircraft on every update.
  // This must run before the early-return below because table rows can repeat
  // rounded coordinates even though we still want to keep the camera locked.
  if (followSelected && selectedAircraft === id && map) {
    try {
      map.panTo([lat, lon], { animate: true });
    } catch (_) {}
  }

  // Check if position actually changed (to avoid redundant updates)
  if (entry.coords.length > 0) {
    const lastCoord = entry.coords[entry.coords.length - 1];
    if (lastCoord.lat === lat && lastCoord.lon === lon) {
      return;
    }
  }

  // Add to track history with metadata
  entry.coords.push({ lat, lon, alt: altValue, speed: speedValue, heading: data.heading || 0 });
  while (entry.coords.length > maxTrackPoints) {
    entry.coords.shift(); // Keep history limited to maxTrackPoints
  }

  // Draw colored polyline using altitude-based color per segment
  if (!entry.polylineGroup) {
    entry.polylineGroup = L.featureGroup();
    // Backward-compat alias: some code may still check entry.polyline
    entry.polyline = entry.polylineGroup;

    if (pathsVisible && layerVisible) {
      entry.polylineGroup.addTo(map);
    }
  }

  // Enforce current visibility state (also covers newly created groups)
  if (entry.polylineGroup) {
    const shouldShowPaths = pathsVisible && layerVisible;
    if (shouldShowPaths) {
      if (!map.hasLayer(entry.polylineGroup)) entry.polylineGroup.addTo(map);
    } else {
      if (map.hasLayer(entry.polylineGroup)) map.removeLayer(entry.polylineGroup);
    }
  }
  
  // Clear old polyline segments
  if (entry.polylineGroup) {
    entry.polylineGroup.clearLayers();
    
    // Create polyline segments with altitude-based colors
    for (let i = 0; i < entry.coords.length - 1; i++) {
      const c1 = entry.coords[i];
      const c2 = entry.coords[i + 1];
      const segColor = colorByAltitude(c1.alt);
      const segment = L.polyline([[c1.lat, c1.lon], [c2.lat, c2.lon]], {
        color: segColor,
        weight: 3,
        opacity: 0.85,
        dashArray: isSimSource ? '6 6' : undefined
      });
      entry.polylineGroup.addLayer(segment);
    }
  }

  // Create or update marker
  const popup = buildAircraftPopupHtml(entry);
  const newColor = colorByAltitude(altValue);
  const heading = data.heading || 0;
  const rotation = `transform: rotate(${heading}deg);`;
  const ringStroke = isSimSource ? 'rgba(197,72,63,0.95)' : 'rgba(30,126,200,0.9)';
  const svg = `<svg xmlns='http://www.w3.org/2000/svg' width='28' height='28' viewBox='0 0 24 24' class='plane-svg' style='${rotation}'>` +
    `<circle cx='12' cy='12' r='11' fill='none' stroke='${ringStroke}' stroke-width='1.8' />` +
    `<polygon points='12,2 4,20 12,15 20,20' fill='${newColor}' stroke='rgba(255,255,255,0.45)' stroke-width='1'/>` +
    `</svg>`;
  const icon = L.divIcon({ className: 'plane-divicon', html: svg, iconSize: [28, 28] });

  if (!entry.marker) {
    entry.marker = L.marker([lat, lon], { icon: icon });
    entry.marker.bindPopup(popup);
    
    // Add click handler to marker to select in table
    entry.marker.on('click', () => {
      selectAircraftByIcao(id, 'online');
    });

    if (layerVisible) {
      entry.marker.addTo(map);
    }
  } else {
    entry.marker.setLatLng([lat, lon]).setIcon(icon).getPopup().setContent(popup);
  }

  // Ensure marker is shown/hidden according to current layer
  if (entry.marker) {
    const has = map.hasLayer(entry.marker);
    if (layerVisible && !has) entry.marker.addTo(map);
    if (!layerVisible && has) map.removeLayer(entry.marker);
  }
  entry.alt = altValue;
  entry.speed = speedValue;
}

// Update aircraft for OFFLINE store (historical data)
function updateAircraftOffline(data) {
  const id = data.icao;
  const lat = data.lat;
  const lon = data.lon;
  const altRaw = data.alt;
  const speedRaw = data.speed;
  const altValue = (typeof altRaw === 'number' && Number.isFinite(altRaw)) ? altRaw : 0;
  const speedValue = (typeof speedRaw === 'number' && Number.isFinite(speedRaw)) ? speedRaw : 0;
  const source = data.source || 'sim';
  const sourceKey = layerKeyForSource(source);
  const isSimSource = sourceKey === 'sim';

  if (!aircraftOffline.has(id)) {
    aircraftOffline.set(id, {
      coords: [],
      marker: null,
      polyline: null,
      polylineGroup: null,
      last: null,
      alt: 0,
      speed: 0,
      source: source
    });
  }

  const entry = aircraftOffline.get(id);
  entry.source = source;
  const layerVisible = isLayerEnabledForSource(source, 'offline');

  entry.last = {
    icao: id,
    callsign: data.callsign,
    lat,
    lon,
    alt: (typeof altRaw === 'number' && Number.isFinite(altRaw)) ? altRaw : null,
    speed: (typeof speedRaw === 'number' && Number.isFinite(speedRaw)) ? speedRaw : null,
    heading: data.heading || 0,
    track: data.track,
    squawk: data.squawk,
    rssi: data.rssi,
    verticalRate: data.verticalRate,
    messages: data.messages,
    onGround: data.onGround,
    source,
    seen: data.seen,
    origin: data.origin,
    geoAltFt: data.geoAltFt,
    baroAltFt: data.baroAltFt,
    velocityMs: data.velocityMs,
  };

  // Track history
  const heading = data.heading || 0;
  entry.coords.push({ lat, lon, alt: altValue, speed: speedValue, heading });
  while (entry.coords.length > maxTrackPoints) entry.coords.shift();

  // Build or update colored polyline segments
  if (!entry.polylineGroup) {
    entry.polylineGroup = L.featureGroup();
  }
  entry.polylineGroup.clearLayers();
  for (let i = 0; i < entry.coords.length - 1; i++) {
    const c1 = entry.coords[i];
    const c2 = entry.coords[i + 1];
    const segColor = colorByAltitude(c1.alt);
    const segment = L.polyline([[c1.lat, c1.lon], [c2.lat, c2.lon]], {
      color: segColor,
      weight: 3,
      opacity: 0.85,
      dashArray: isSimSource ? '6 6' : undefined
    });
    entry.polylineGroup.addLayer(segment);
  }

  if (pathsVisible && layerVisible) {
    if (!map.hasLayer(entry.polylineGroup)) entry.polylineGroup.addTo(map);
  } else {
    if (map.hasLayer(entry.polylineGroup)) map.removeLayer(entry.polylineGroup);
  }

  // Marker
  const popup = buildAircraftPopupHtml(entry);
  const newColor = colorByAltitude(altValue);
  const rotation = `transform: rotate(${heading}deg);`;
  const ringStroke = isSimSource ? 'rgba(197,72,63,0.95)' : 'rgba(30,126,200,0.9)';
  const svg = `<svg xmlns='http://www.w3.org/2000/svg' width='28' height='28' viewBox='0 0 24 24' class='plane-svg' style='${rotation}'>` +
    `<circle cx='12' cy='12' r='11' fill='none' stroke='${ringStroke}' stroke-width='1.8' />` +
    `<polygon points='12,2 4,20 12,15 20,20' fill='${newColor}' stroke='rgba(255,255,255,0.45)' stroke-width='1'/>` +
    `</svg>`;
  const icon = L.divIcon({ className: 'plane-divicon', html: svg, iconSize: [28, 28] });

  if (!entry.marker) {
    entry.marker = L.marker([lat, lon], { icon: icon });
    entry.marker.bindPopup(popup);
    entry.marker.on('click', () => {
      selectAircraftByIcao(id, 'offline');
    });
    if (layerVisible) {
      entry.marker.addTo(map);
    }
  } else {
    entry.marker.setLatLng([lat, lon]).setIcon(icon).getPopup().setContent(popup);
  }

  if (entry.marker) {
    const has = map.hasLayer(entry.marker);
    if (layerVisible && !has) entry.marker.addTo(map);
    if (!layerVisible && has) map.removeLayer(entry.marker);
  }

  entry.alt = altValue;
  entry.speed = speedValue;
}

function formatMaybeNumber(value, suffix) {
  if (value === undefined || value === null) return '-';
  if (typeof value === 'number' && Number.isNaN(value)) return '-';
  const s = value.toString();
  return suffix ? `${s} ${suffix}` : s;
}

function formatMaybeText(value) {
  if (value === undefined || value === null) return '-';
  const s = value.toString().trim();
  return s === '' ? '-' : s;
}

function formatSeen(seen) {
  const s = formatMaybeText(seen);
  if (s === '-') return '-';
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return s;
  return d.toLocaleString();
}

function buildAircraftPopupHtml(entry) {
  const d = entry?.last || {};
  const lat = typeof d.lat === 'number' ? d.lat.toFixed(5) : formatMaybeText(d.lat);
  const lon = typeof d.lon === 'number' ? d.lon.toFixed(5) : formatMaybeText(d.lon);

  function kv(label, value) {
    return {
      label,
      value
    };
  }

  function tr(left, right) {
    const labelStyle = 'padding:2px 6px 2px 0;white-space:nowrap;opacity:0.9;color:#8fc6ff;vertical-align:top;';
    const valueStyle = 'padding:2px 10px 2px 0;color:#b7d9ff;vertical-align:top;';
    const labelStyleR = 'padding:2px 6px 2px 10px;white-space:nowrap;opacity:0.9;color:#8fc6ff;vertical-align:top;border-left:1px solid rgba(30,126,200,0.35);';
    const valueStyleR = 'padding:2px 0 2px 0;color:#b7d9ff;vertical-align:top;';
    return `<tr>` +
      `<td style="${labelStyle}">${left.label}</td>` +
      `<td style="${valueStyle}">${left.value}</td>` +
      `<td style="${labelStyleR}">${right.label}</td>` +
      `<td style="${valueStyleR}">${right.value}</td>` +
    `</tr>`;
  }

  const rows = [
    tr(kv('Callsign', formatMaybeText(d.callsign)), kv('Source', formatMaybeText(d.source))),
    tr(kv('Seen', formatSeen(d.seen)), kv('OnGround', d.onGround === true ? 'yes' : (d.onGround === false ? 'no' : '-'))),
    tr(kv('Lat', lat), kv('Lon', lon)),
    tr(kv('Alt', formatMaybeNumber(d.alt, 'ft')), kv('Speed', formatMaybeNumber(d.speed, 'kt'))),
    tr(kv('GeoAlt', formatMaybeNumber(d.geoAltFt, 'ft')), kv('BaroAlt', formatMaybeNumber(d.baroAltFt, 'ft'))),
    tr(kv('Heading', formatMaybeNumber(d.heading, '°')), kv('Track', formatMaybeNumber(d.track, '°'))),
    tr(kv('VRate', formatMaybeNumber(d.verticalRate, 'ft/min')), kv('Squawk', formatMaybeText(d.squawk))),
    tr(kv('RSSI', formatMaybeNumber(d.rssi, 'dBm')), kv('Messages', formatMaybeNumber(d.messages))),
    tr(kv('Velocity', formatMaybeNumber(d.velocityMs, 'm/s')), kv('Origin', formatMaybeText(d.origin))),
  ].join('');

  return `
    <div class="track-popup">
      <div style="font-weight:bold;margin-bottom:6px;">${formatMaybeText(d.icao)}</div>
      <table style="border-collapse:collapse;font-size:12px;line-height:1.25;">
        ${rows}
      </table>
    </div>
  `;
}

// Select aircraft by ICAO and highlight in table and map
function selectAircraftByIcao(icao, mode) {
  selectedAircraft = icao;

  const preferredMode = (mode === 'offline' || mode === 'online') ? mode : activeDataTab;
  const tbody = preferredMode === 'offline'
    ? document.getElementById('adsb-offline-body')
    : document.getElementById('adsb-debug-body');

  // Highlight row in active table
  if (tbody) {
    tbody.querySelectorAll('tr').forEach(row => row.classList.remove('selected'));

    let targetRow = null;
    if (preferredMode !== 'offline') {
      targetRow = document.getElementById(`aircraft-${icao}`);
    }
    if (!targetRow) {
      for (const row of tbody.querySelectorAll('tr')) {
        const cell = row.querySelector('td:first-child');
        const rowIcao = (cell?.textContent || '').trim();
        if (rowIcao === icao) {
          targetRow = row;
          break;
        }
      }
    }
    if (targetRow) {
      targetRow.classList.add('selected');
      targetRow.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    }
  }

  // Highlight marker on map and zoom (prefer the store matching mode)
  const stores = preferredMode === 'offline'
    ? [aircraftOffline, aircraft]
    : [aircraft, aircraftOffline];

  for (const store of stores) {
    if (!store || !store.has(icao)) continue;
    const entry = store.get(icao);
    if (entry && entry.marker) {
      entry.marker.openPopup();
      map.setView(entry.marker.getLatLng(), 10, { animate: true });
      break;
    }
  }
}

// Add altitude legend (only if map is ready)
function addLegend() {
  if (!map) return;
  const legend = L.control({ position: 'bottomright' });
  legend.onAdd = () => {
    const div = L.DomUtil.create('div', 'altitude-legend');
    div.innerHTML = `
      <div style="background:rgba(15,16,25,0.95);color:#e0e0e0;padding:8px;border-radius:6px;font-family:Arial,Helvetica,sans-serif;font-size:11px;width:120px;border:1px solid #1e7ec8;">
        <strong style="font-size:10px;">Altitude (ft)</strong><br/>
        <div style="height:24px;background:linear-gradient(to right, rgb(0,0,255), rgb(0,255,255), rgb(0,255,0), rgb(255,255,0), rgb(255,0,0));border-radius:4px;margin:4px 0;"></div>
        <div style="display:flex;justify-content:space-between;font-size:9px;">
          <span>0</span>
          <span>23k</span>
          <span>45k</span>
        </div>
      </div>
    `;
    L.DomEvent.disableClickPropagation(div);
    return div;
  };
  legend.addTo(map);
}

// Initialize legend after map is ready
setTimeout(() => {
  if (map && typeof addLegend === 'function') addLegend();
}, 100);

// Initialize overlay buttons and table sorting after DOM is ready
function onClearTracksClick() {
  clearTracks();
}

function onTogglePathsClick() {
  togglePathsVisible();
}

function onFollowClick() {
  followSelected = !followSelected;
  saveFollowToStorage();
  updateFollowButtonUi();
  followSelectedNow();
}

function initializeButtons() {
  const clearBtn = document.getElementById('adsb-clear-tracks');
  if (clearBtn) {
    clearBtn.removeEventListener('click', onClearTracksClick);
    clearBtn.addEventListener('click', onClearTracksClick);
  }

  const toggleBtn = document.getElementById('adsb-toggle-polys');
  if (toggleBtn) {
    toggleBtn.removeEventListener('click', onTogglePathsClick);
    toggleBtn.addEventListener('click', onTogglePathsClick);
  }

  const followBtn = document.getElementById('adsb-follow');
  if (followBtn) {
    followBtn.removeEventListener('click', onFollowClick);
    followBtn.addEventListener('click', onFollowClick);
  }

  updateFollowButtonUi();
  
  // Initialize table column sorting
  initializeTableSorting();
}

// Table sorting functionality
/**
 * Initialize table header click handlers for sorting.
 * Each column header with data-sort attribute becomes sortable.
 */
function initializeTableSorting() {
  const table = document.getElementById('adsb-debug-table');
  if (!table) return;

  const thead = table.querySelector('thead');
  if (!thead) return;

  // Idempotent: don't stack multiple listeners on repeated init
  thead.removeEventListener('click', handleHeaderClick);
  thead.addEventListener('click', handleHeaderClick);

  // Cosmetic: ensure headers show pointer
  table.querySelectorAll('th[data-sort]').forEach(h => {
    h.style.cursor = 'pointer';
  });

  const tbody = document.getElementById('adsb-debug-body');
  if (tbody) {
    tbody.removeEventListener('click', handleTableBodyClick);
    tbody.addEventListener('click', handleTableBodyClick);

    tbody.removeEventListener('dblclick', handleTableBodyDblClick);
    tbody.addEventListener('dblclick', handleTableBodyDblClick);
  }

  updateSortIndicators();
}

function handleHeaderClick(event) {
  const header = event.target.closest('th[data-sort]');
  if (!header) return;
  handleSort(header.getAttribute('data-sort'));
}

/**
 * Handle column sort. Toggle direction if same column, else sort ascending.
 * @param {string} sortKey - Column name (id, callsign, alt, spd, hdg, squawk, rssi)
 */
function handleSort(sortKey) {
  // Toggle sort direction if clicking same column, else set to ascending
  if (currentSortKey === sortKey) {
    currentSortAsc = !currentSortAsc;
  } else {
    currentSortKey = sortKey;
    currentSortAsc = true;
  }
  
  // Update UI immediately and request new data
  updateSortIndicators();
  updateHTMXSort();
}

function updateHTMXSort() {
  fetchAndUpdateTable();
}

/**
 * Fetch aircraft table data from server with current sort parameters.
 * Sends a GET request to /api/aircraft-rows with sort=column&asc=true|false.
 * Updates the table tbody with returned HTML rows.
 */
function fetchAndUpdateTable() {
  const baseUrl = 'http://localhost:8080/api/aircraft-rows';
  const sortUrl = currentSortKey 
    ? `${baseUrl}?sort=${currentSortKey}&asc=${currentSortAsc}`
    : baseUrl;
  
  console.log('[POLL] Fetching:', sortUrl, 'currentSortAsc=', currentSortAsc);
  
  fetch(sortUrl)
    .then(response => response.text())
    .then(html => {
      const tbody = document.getElementById('adsb-debug-body');
      if (tbody) {
        tbody.innerHTML = html;
        triggerHTMXAfterSettle();
        updateSortIndicators();
        applyTableFilter();
      }
    })
    .catch(error => console.error('[POLL] Error fetching:', error));
}

/**
 * Manually trigger HTMX afterSettle event processing.
 * This simulates what HTMX does automatically: parse table rows and update map.
 */
function triggerHTMXAfterSettle() {
  if (!map || !aircraft) return;
  
  const tbody = document.getElementById('adsb-debug-body');
  const rows = tbody.querySelectorAll('tr');
  
  rows.forEach(row => {
    const lat = parseFloat(row.dataset.lat);
    const lon = parseFloat(row.dataset.lon);
    const source = row.dataset.source || 'sim';
    
    const cells = row.querySelectorAll('td');
    if (cells.length >= 8 && !isNaN(lat) && !isNaN(lon)) {
      const icao = cells[0].textContent.trim();
      const callsign = cells[1].textContent.trim();
      const alt = parseInt(cells[2].textContent);
      const speed = parseInt(cells[3].textContent);
      const heading = parseInt(cells[4].textContent);
      
      updateAircraft({
        icao, lat, lon, alt, speed, heading, callsign,
        source,
        seen: new Date().toISOString()
      });
    }
  });
  
  // Restore selection highlighting after table update
  if (selectedAircraft) {
    const selectedRow = document.getElementById(`aircraft-${selectedAircraft}`);
    if (selectedRow) {
      selectedRow.classList.add('selected');
    }
  }

  // Keep current search filter applied
  applyTableFilter();

  // Keep current layer visibility applied
  applyLayerVisibilityToMap();
}

// --- Offline query (Postgres) ---

function getOfflineRequireFieldsSelection() {
  const fields = [];
  const addIfChecked = (id, key) => {
    const el = document.getElementById(id);
    if (el && el.checked) fields.push(key);
  };
  addIfChecked('adsb-offline-require-callsign', 'callsign');
  addIfChecked('adsb-offline-require-squawk', 'squawk');
  addIfChecked('adsb-offline-require-origin', 'origin_country');
  return fields;
}

function toRFC3339FromDatetimeLocal(value) {
  if (!value) return '';
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return '';
  return d.toISOString();
}

async function runHistoryQueryFromUI() {
  // Backwards-compat: old History UI removed.
}

function escapeHtml(s) {
  return (s || '').toString()
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#039;');
}

function clearOfflineResults() {
  const tbody = document.getElementById('adsb-offline-body');
  if (tbody) tbody.innerHTML = '';

  if (map && aircraftOffline) {
    for (const entry of aircraftOffline.values()) {
      if (entry.marker && map.hasLayer(entry.marker)) map.removeLayer(entry.marker);
      if (entry.polylineGroup && map.hasLayer(entry.polylineGroup)) map.removeLayer(entry.polylineGroup);
    }
  }
  if (aircraftOffline) aircraftOffline.clear();
}

function coerceNumber(v) {
  if (v === undefined || v === null) return null;
  if (typeof v === 'number') return Number.isFinite(v) ? v : null;
  const n = Number(v);
  return Number.isFinite(n) ? n : null;
}

function renderOfflineFromAggregates(aggMap) {
  if (!map || !aircraftOffline) return;

  // Build map layers efficiently: one pass per aircraft
  for (const [icao, agg] of aggMap.entries()) {
    if (!agg || !agg.last || !Array.isArray(agg.coords) || agg.coords.length === 0) continue;

    const last = agg.last;
    const source = last.source || 'simulator';
    const sourceKey = layerKeyForSource(source);
    const isSimSource = sourceKey === 'sim';
    const layerVisible = isLayerEnabledForSource(source, 'offline');

    const entry = {
      coords: agg.coords,
      marker: null,
      polyline: null,
      polylineGroup: null,
      last: {
        icao,
        callsign: last.callsign,
        lat: last.lat,
        lon: last.lon,
        alt: last.alt,
        speed: last.speed,
        heading: last.heading,
        track: last.track,
        squawk: last.squawk,
        rssi: last.rssi,
        verticalRate: last.verticalRate,
        messages: last.messages,
        onGround: last.onGround,
        source,
        seen: last.seen,
        origin: last.origin,
        geoAltFt: last.geoAltFt,
        baroAltFt: last.baroAltFt,
        velocityMs: last.velocityMs,
      },
      alt: coerceNumber(last.alt) ?? 0,
      speed: coerceNumber(last.speed) ?? 0,
      source,
    };

    // Track polyline segments (build once)
    entry.polylineGroup = L.featureGroup();
    for (let i = 0; i < entry.coords.length - 1; i++) {
      const c1 = entry.coords[i];
      const c2 = entry.coords[i + 1];
      const segColor = colorByAltitude(c1.alt || 0);
      const segment = L.polyline([[c1.lat, c1.lon], [c2.lat, c2.lon]], {
        color: segColor,
        weight: 3,
        opacity: 0.85,
        dashArray: isSimSource ? '6 6' : undefined
      });
      entry.polylineGroup.addLayer(segment);
    }
    if (pathsVisible && layerVisible) {
      entry.polylineGroup.addTo(map);
    }

    // Marker
    const altValue = coerceNumber(last.alt) ?? 0;
    const newColor = colorByAltitude(altValue);
    const heading = coerceNumber(last.heading) ?? 0;
    const rotation = `transform: rotate(${heading}deg);`;
    const ringStroke = isSimSource ? 'rgba(197,72,63,0.95)' : 'rgba(30,126,200,0.9)';
    const svg = `<svg xmlns='http://www.w3.org/2000/svg' width='28' height='28' viewBox='0 0 24 24' class='plane-svg' style='${rotation}'>` +
      `<circle cx='12' cy='12' r='11' fill='none' stroke='${ringStroke}' stroke-width='1.8' />` +
      `<polygon points='12,2 4,20 12,15 20,20' fill='${newColor}' stroke='rgba(255,255,255,0.45)' stroke-width='1'/>` +
      `</svg>`;
    const icon = L.divIcon({ className: 'plane-divicon', html: svg, iconSize: [28, 28] });

    entry.marker = L.marker([last.lat, last.lon], { icon: icon });
    entry.marker.bindPopup(buildAircraftPopupHtml(entry));
    entry.marker.on('click', () => selectAircraftByIcao(icao, 'offline'));
    if (layerVisible) {
      entry.marker.addTo(map);
    }

    aircraftOffline.set(icao, entry);
  }
}

function buildOfflineTableHtml(rows) {
  const fmtNum = (v) => {
    if (v === undefined || v === null) return '';
    if (typeof v === 'number' && Number.isNaN(v)) return '';
    return v.toString();
  };
  const fmtTxt = (v) => {
    if (v === undefined || v === null) return '';
    const s = v.toString().trim();
    return s;
  };
  const fmtSeen = (v) => {
    const s = fmtTxt(v);
    if (!s) return '';
    const d = new Date(s);
    if (Number.isNaN(d.getTime())) return s;
    return d.toLocaleString();
  };

  return rows.map((r) => {
    const icao = escapeHtml(fmtTxt(r.icao));
    const callsign = escapeHtml(fmtTxt(r.callsign));
    const alt = escapeHtml(fmtNum(r.alt));
    const speed = escapeHtml(fmtNum(r.speed));
    const heading = escapeHtml(fmtNum(r.heading));
    const squawk = escapeHtml(fmtTxt(r.squawk));
    const rssi = escapeHtml(fmtNum(r.rssi));
    const seen = escapeHtml(fmtSeen(r.seen));
    const lat = (typeof r.lat === 'number' && Number.isFinite(r.lat)) ? r.lat : '';
    const lon = (typeof r.lon === 'number' && Number.isFinite(r.lon)) ? r.lon : '';
    const source = escapeHtml(fmtTxt(r.source || ''));
    return `
      <tr data-source="${source}" data-lat="${lat}" data-lon="${lon}">
        <td style="text-align:left;padding:4px 6px;">${icao}</td>
        <td style="text-align:left;padding:4px 6px;">${callsign}</td>
        <td style="text-align:right;padding:4px 6px;">${alt}</td>
        <td style="text-align:right;padding:4px 6px;">${speed}</td>
        <td style="text-align:right;padding:4px 6px;">${heading}</td>
        <td style="text-align:right;padding:4px 6px;">${squawk}</td>
        <td style="text-align:right;padding:4px 6px;">${rssi}</td>
        <td style="text-align:left;padding:4px 6px;">${seen}</td>
      </tr>`;
  }).join('');
}

async function runOfflineQueryFromUI() {
  const status = document.getElementById('adsb-offline-status');
  const setStatus = (s) => { if (status) status.textContent = s; };

  const sourceEl = document.getElementById('adsb-offline-source');
  const fromEl = document.getElementById('adsb-offline-from');
  const toEl = document.getElementById('adsb-offline-to');

  const source = sourceEl ? sourceEl.value : '';
  const from = toRFC3339FromDatetimeLocal(fromEl && fromEl.value);
  const to = toRFC3339FromDatetimeLocal(toEl && toEl.value);

  if (!from || !to) {
    setStatus('Bitte Zeitraum wählen');
    return;
  }

  const requireFields = getOfflineRequireFieldsSelection();
  const params = new URLSearchParams();
  params.set('source', source);
  params.set('from', from);
  params.set('to', to);
  if (requireFields.length > 0) params.set('fields', requireFields.join(','));

  const fromDate = new Date(from);
  const toDate = new Date(to);
  const windowMs = (Number.isFinite(fromDate.getTime()) && Number.isFinite(toDate.getTime()))
    ? (toDate.getTime() - fromDate.getTime())
    : Number.POSITIVE_INFINITY;

  // For large windows, avoid huge payloads: fetch only the latest report per ICAO.
  // For small windows, fetch full history to draw tracks.
  const wantFullHistory = windowMs <= 60 * 60 * 1000; // <= 1 hour

  if (wantFullHistory) {
    params.set('limit', '20000');
  } else {
    params.set('limit', '5000');
  }

  const endpoint = wantFullHistory ? '/api/history' : '/api/history/latest';
  const url = `http://localhost:8080${endpoint}?${params.toString()}`;
  setStatus('Lade...');

  try {
    const res = await fetch(url);
    if (!res.ok) {
      const text = await res.text();
      console.error('[HISTORY] Error:', res.status, text);
      setStatus(`Fehler (${res.status})`);
      return;
    }

    const rows = await res.json();
    if (!Array.isArray(rows)) {
      setStatus('Unerwartete Antwort');
      return;
    }

    clearOfflineResults();

    if (wantFullHistory) {
      // Aggregate per aircraft (table shows latest per ICAO; map draws track per ICAO)
      const agg = new Map();
      let totalPoints = 0;
      let reportCount = 0;

      for (const r of rows) {
        if (!r || typeof r !== 'object') continue;
        const icao = (r.icao || '').toString().trim();
        const lat = coerceNumber(r.lat);
        const lon = coerceNumber(r.lon);
        if (!icao || lat === null || lon === null) {
          reportCount++;
          continue;
        }

        let a = agg.get(icao);
        if (!a) {
          a = { coords: [], last: null };
          agg.set(icao, a);
        }

        if (totalPoints < OFFLINE_MAX_TOTAL_POINTS) {
          a.coords.push({ lat, lon, alt: coerceNumber(r.alt) ?? 0 });
          totalPoints++;
          if (a.coords.length > OFFLINE_MAX_POINTS_PER_AIRCRAFT) {
            a.coords = a.coords.slice(a.coords.length - OFFLINE_MAX_POINTS_PER_AIRCRAFT);
          }
        }

        a.last = {
          icao,
          lat,
          lon,
          seen: r.seen,
          source: r.source,
          alt: r.alt,
          speed: r.speed,
          heading: r.heading,
          callsign: r.callsign,
          squawk: r.squawk,
          rssi: r.rssi,
          verticalRate: r.verticalRate,
          messages: r.messages,
          onGround: r.onGround,
          origin: r.origin,
          geoAltFt: r.geoAltFt,
          baroAltFt: r.baroAltFt,
          velocityMs: r.velocityMs,
          track: r.track,
        };

        reportCount++;
      }

      renderOfflineFromAggregates(agg);
      applyLayerVisibilityToMap();

      const latestRows = [];
      for (const a of agg.values()) {
        if (a && a.last) latestRows.push(a.last);
      }

      const tbody = document.getElementById('adsb-offline-body');
      if (tbody) {
        tbody.innerHTML = buildOfflineTableHtml(latestRows);
        tbody.removeEventListener('click', handleTableBodyClick);
        tbody.addEventListener('click', handleTableBodyClick);
        tbody.removeEventListener('dblclick', handleTableBodyDblClick);
        tbody.addEventListener('dblclick', handleTableBodyDblClick);
      }

      applyTableFilter();
      setStatus(`${latestRows.length} Flugzeuge • ${reportCount} Meldungen`);
      return;
    }

    // Latest-per-ICAO mode
    let rowCount = 0;
    for (const r of rows) {
      if (!r || typeof r !== 'object') continue;
      const icao = (r.icao || '').toString().trim();
      const lat = coerceNumber(r.lat);
      const lon = coerceNumber(r.lon);
      if (!icao || lat === null || lon === null) continue;

      updateAircraftOffline({
        icao,
        lat,
        lon,
        alt: coerceNumber(r.alt) ?? 0,
        speed: coerceNumber(r.speed),
        heading: coerceNumber(r.heading),
        callsign: r.callsign,
        squawk: r.squawk,
        rssi: coerceNumber(r.rssi),
        verticalRate: coerceNumber(r.verticalRate),
        messages: coerceNumber(r.messages),
        onGround: r.onGround,
        origin: r.origin,
        geoAltFt: coerceNumber(r.geoAltFt),
        baroAltFt: coerceNumber(r.baroAltFt),
        velocityMs: coerceNumber(r.velocityMs),
        source: r.source,
        seen: r.seen,
      });
      rowCount++;
    }
    applyLayerVisibilityToMap();

    const tbody = document.getElementById('adsb-offline-body');
    if (tbody) {
      tbody.innerHTML = buildOfflineTableHtml(rows);
      tbody.removeEventListener('click', handleTableBodyClick);
      tbody.addEventListener('click', handleTableBodyClick);
      tbody.removeEventListener('dblclick', handleTableBodyDblClick);
      tbody.addEventListener('dblclick', handleTableBodyDblClick);
    }

    applyTableFilter();

    setStatus(`${rowCount} Flugzeuge (Latest je ICAO)`);
  } catch (e) {
    console.error('[HISTORY] Exception:', e);
    setStatus('Fehler');
  }
}

// Wire up Offline UI
window.addEventListener('load', () => {
  const btn = document.getElementById('adsb-offline-run');
  if (!btn) return;
  btn.addEventListener('click', (e) => {
    e.preventDefault();
    setActiveTab('offline');
    runOfflineQueryFromUI();
  });
});

/**
 * Update sort column indicator UI (▲/▼ symbols).
 * Adds 'sort-asc' or 'sort-desc' class to active header.
 */
function updateSortIndicators() {
  const table = document.getElementById('adsb-debug-table');
  if (!table) return;
  
  const thead = table.querySelector('thead');
  thead.querySelectorAll('th[data-sort]').forEach(h => {
    h.classList.remove('sort-asc', 'sort-desc');
  });
  
  if (currentSortKey) {
    const header = thead.querySelector(`th[data-sort="${currentSortKey}"]`);
    if (header) {
      header.classList.add(currentSortAsc ? 'sort-asc' : 'sort-desc');
    }
  }
}

/**
 * Handle sort by column. Called when table header is clicked.
 * Toggles sort direction if same column clicked twice.
 * @param {string} sortKey - Column to sort by (id, callsign, alt, spd, hdg, squawk, rssi)
 */

// Handle table body click with event delegation for row selection
function handleTableBodyClick(event) {
  const row = event.target.closest('tbody tr');
  if (!row) return;
  
  const icao = row.querySelector('td:first-child')?.textContent?.trim() || '';
  if (icao) {
    selectAircraftByIcao(icao);
  }
}

function handleTableBodyDblClick(event) {
  const row = event.target.closest('tbody tr');
  if (!row) return;

  const icao = row.querySelector('td:first-child')?.textContent?.trim() || '';
  if (!icao) return;

  selectAircraftByIcao(icao);
  followSelected = true;
  saveFollowToStorage();
  updateFollowButtonUi();
  followSelectedNow();
}

console.log('[ADSB] Map client ready. Polling data via HTMX...');

// Start polling loop that respects sort parameters
document.addEventListener('DOMContentLoaded', function() {
  // Initial fetch
  fetchAndUpdateTable();
  
  // Poll every 1 second, always using current sort parameters
  setInterval(() => {
    fetchAndUpdateTable();
  }, 1000);
});
