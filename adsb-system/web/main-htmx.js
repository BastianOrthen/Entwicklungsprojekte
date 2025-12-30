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
let selectedAircraft = null;  // Currently selected aircraft ICAO
let currentSortKey = null;    // Current sort column (e.g., "alt", "spd")
let currentSortAsc = true;    // Sort direction: true = ascending (▲), false = descending (▼)
const MAX_TRACK_POINTS = 200; // Max polyline points per aircraft

console.log('[ADSB] Script loaded, waiting for DOM...');

// Initialize map and UI after DOM is ready
document.addEventListener('DOMContentLoaded', function() {
  console.log('[ADSB] DOM ready, initializing map...');
  
  const center = [50, 8];      // Central Europe
  const zoom = 6;

  // Initialize Leaflet map
  map = L.map('map').setView(center, zoom);
  L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
    maxZoom: 19,
  }).addTo(map);

  // Initialize aircraft tracking store: ICAO -> {polyline, marker, coords[]}
  aircraft = new Map();
  
  // Initialize table sorting UI
  initializeTableSorting();
  
  console.log('[ADSB] Map initialized, ready for HTMX events');
});

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
      
      // Read cells: ICAO, Callsign, Alt, Spd, Hdg, SQK, RSSI, Pred
      const cells = row.querySelectorAll('td');
      if (cells.length >= 8 && !isNaN(lat) && !isNaN(lon)) {
        const icao = cells[0].textContent.trim();
        const callsign = cells[1].textContent.trim();
        const alt = parseInt(cells[2].textContent);
        const speed = parseInt(cells[3].textContent);
        const heading = parseInt(cells[4].textContent);
        
        console.log('[HTMX] Processing aircraft:', icao, callsign, 'at', lat, lon, 'alt', alt);
        updateAircraft({
          icao, lat, lon, alt, speed, heading,
          callsign,
          seen: new Date().toISOString()
        });
      }
    });
    
    console.log('[HTMX] Done processing. Total aircraft on map:', aircraft.size);
    
    // Attach delegated click handlers to tbody for row selection
    tbody.removeEventListener('click', handleTableBodyClick);
    tbody.addEventListener('click', handleTableBodyClick);
    
    // Reinitialize table sorting UI
    initializeTableSorting();
    
    // Reapply selection highlighting
    if (selectedAircraft) {
      const selectedRow = document.getElementById(`aircraft-${selectedAircraft}`);
      if (selectedRow) {
        selectedRow.classList.add('selected');
        console.log('[HTMX] Selection restored for:', selectedAircraft);
      }
    }
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
  const alt = data.alt || 0;
  const speed = data.speed || 0;

  if (!aircraft.has(id)) {
    aircraft.set(id, {
      coords: [],          // Track history [{lat, lon, alt, speed, heading}]
      marker: null,        // Leaflet marker for current position
      polyline: null,      // Leaflet polyline for track history
      polylineGroup: null, // FeatureGroup for multi-color polyline segments
      last: null,          // Last known position
      alt: 0,
      speed: 0
    });
  }

  const entry = aircraft.get(id);

  // Check if position actually changed (to avoid redundant updates)
  if (entry.coords.length > 0) {
    const lastCoord = entry.coords[entry.coords.length - 1];
    if (lastCoord.lat === lat && lastCoord.lon === lon) {
      return;
    }
  }

  // Add to track history with metadata
  entry.coords.push({ lat, lon, alt, speed, heading: data.heading || 0 });
  if (entry.coords.length > MAX_TRACK_POINTS) {
    entry.coords.shift(); // Keep history limited to MAX_TRACK_POINTS
  }

  // Draw colored polyline using altitude-based color per segment
  if (!entry.polyline) {
    const polylineGroup = L.featureGroup();
    entry.polylineGroup = polylineGroup;
    polylineGroup.addTo(map);
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
        weight: 2,
        opacity: 0.6
      });
      entry.polylineGroup.addLayer(segment);
    }
  }

  // Create or update marker
  const popup = `<strong>${id}</strong><br/>Alt: ${alt} ft<br/>Spd: ${speed} kt<br/>Hdg: ${data.heading || 0}°`;
  const newColor = colorByAltitude(alt);
  const heading = data.heading || 0;
  const rotation = `transform: rotate(${heading}deg);`;
  const svg = `<svg xmlns='http://www.w3.org/2000/svg' width='28' height='28' viewBox='0 0 24 24' style='${rotation}'><polygon points='12,2 4,20 12,15 20,20' fill='${newColor}'/></svg>`;
  const icon = L.divIcon({ className: 'plane-divicon', html: svg, iconSize: [28, 28] });

  if (!entry.marker) {
    entry.marker = L.marker([lat, lon], { icon: icon }).addTo(map);
    entry.marker.bindPopup(popup);
    
    // Add click handler to marker to select in table
    entry.marker.on('click', () => {
      selectAircraftByIcao(id);
    });
  } else {
    entry.marker.setLatLng([lat, lon]).setIcon(icon).getPopup().setContent(popup);
  }

  entry.last = { alt, speed };
  entry.alt = alt;
  entry.speed = speed;
}

// Select aircraft by ICAO and highlight in table and map
function selectAircraftByIcao(icao) {
  selectedAircraft = icao;
  
  // Highlight row in table
  const tbody = document.getElementById('adsb-debug-body');
  if (tbody) {
    tbody.querySelectorAll('tr').forEach(row => {
      row.classList.remove('selected');
      if (row.id === `aircraft-${icao}`) {
        row.classList.add('selected');
        row.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
      }
    });
  }
  
  // Highlight marker on map and zoom
  if (aircraft.has(icao) && aircraft.get(icao).marker) {
    const entry = aircraft.get(icao);
    entry.marker.openPopup();
    map.setView(entry.marker.getLatLng(), 10, { animate: true });
  }
}

// Add altitude legend (only if map is ready)
function addLegend() {
  if (!map) return;
  const legend = L.control({ position: 'bottomright' });
  legend.onAdd = () => {
    const div = L.DomUtil.create('div', 'altitude-legend');
    div.innerHTML = `
      <div style="background:rgba(0,0,0,0.7);color:#fff;padding:8px;border-radius:6px;font-family:Arial,Helvetica,sans-serif;font-size:11px;width:120px;">
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

// Overlay buttons
const clearBtn = document.getElementById('adsb-clear-tracks');
if (clearBtn) {
  clearBtn.addEventListener('click', () => {
    if (typeof aircraft !== 'undefined') {
      for (const entry of aircraft.values()) {
        if (entry.polyline) map.removeLayer(entry.polyline);
      }
      aircraft.clear();
    }
  });
}

const toggleBtn = document.getElementById('adsb-toggle-polys');
if (toggleBtn) {
  toggleBtn.addEventListener('click', () => {
    if (typeof aircraft !== 'undefined') {
      for (const entry of aircraft.values()) {
        if (entry.polyline) {
          if (map.hasLayer(entry.polyline)) {
            map.removeLayer(entry.polyline);
          } else {
            map.addLayer(entry.polyline);
          }
        }
      }
    }
  });
}

// Initialize overlay buttons and table sorting after DOM is ready
function initializeButtons() {
  const hideBtn = document.getElementById('adsb-hide-overlay');
  if (hideBtn) {
    hideBtn.addEventListener('click', () => {
      const overlay = document.getElementById('adsb-debug-overlay');
      if (overlay) overlay.style.display = 'none';
    });
  }
  
  // Initialize table column sorting
  initializeTableSorting();
}

// Table sorting functionality
function initializeTableSorting() {
  const table = document.getElementById('adsb-debug-table');
  if (!table) return;
  
  const thead = table.querySelector('thead');
  if (!thead) return;
  
  // Remove old event listener if exists
  thead.removeEventListener('click', handleHeaderClick);
  
  // Add single delegated listener on thead
  thead.addEventListener('click', handleHeaderClick);
  
  // Update sort indicators based on current state
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

// Handle header click with event delegation
function handleHeaderClick(event) {
  const header = event.target.closest('th[data-sort]');
  if (!header) return;
  
  const sortKey = header.dataset.sort;
  console.log('[SORT] Header clicked:', sortKey);
  
  // Toggle sort direction if clicking same column, else set to ascending
  if (currentSortKey === sortKey) {
    currentSortAsc = !currentSortAsc;
  } else {
    currentSortKey = sortKey;
    currentSortAsc = true;
  }
  
  // Update HTMX endpoint with sort parameters
  updateHTMXSort();
}

/**
 * Apply sorting to current table state.
 * Immediately fetches sorted data with current sort parameters.
 */
function updateHTMXSort() {
  const tbody = document.getElementById('adsb-debug-body');
  if (!tbody) return;
  
  fetchAndUpdateTable();
  updateSortIndicators();
}

/**
 * Initialize table header click handlers for sorting.
 * Each column header with data-sort attribute becomes sortable.
 */
function initializeTableSorting() {
  const table = document.getElementById('adsb-debug-table');
  if (!table) return;
  
  const headers = table.querySelectorAll('th[data-sort]');
  headers.forEach(h => {
    h.addEventListener('click', function() {
      const sortKey = this.getAttribute('data-sort');
      handleSort(sortKey);
    });
    h.style.cursor = 'pointer';
  });
  
  const tbody = document.getElementById('adsb-debug-body');
  if (tbody) {
    tbody.addEventListener('click', handleTableBodyClick);
  }
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
  
  // Update HTMX endpoint with sort parameters
  updateHTMXSort();
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
  
  console.log('[POLL] Fetching:', sortUrl);
  
  fetch(sortUrl)
    .then(response => response.text())
    .then(html => {
      const tbody = document.getElementById('adsb-debug-body');
      if (tbody) {
        tbody.innerHTML = html;
        triggerHTMXAfterSettle();
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
    
    const cells = row.querySelectorAll('td');
    if (cells.length >= 8 && !isNaN(lat) && !isNaN(lon)) {
      const icao = cells[0].textContent.trim();
      const callsign = cells[1].textContent.trim();
      const alt = parseInt(cells[2].textContent);
      const speed = parseInt(cells[3].textContent);
      const heading = parseInt(cells[4].textContent);
      
      updateAircraft({
        icao, lat, lon, alt, speed, heading, callsign,
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
}

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

// Ensure buttons are initialized
setTimeout(() => {
  if (document.getElementById('adsb-hide-overlay')) {
    initializeButtons();
  }
}, 100);

// Handle table body click with event delegation for row selection
function handleTableBodyClick(event) {
  const row = event.target.closest('tbody tr');
  if (!row) return;
  
  const icao = row.querySelector('td:first-child')?.textContent?.trim() || '';
  if (icao) {
    selectAircraftByIcao(icao);
  }
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
