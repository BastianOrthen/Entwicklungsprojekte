// ADSB Map Client with HTMX: connects to HTMX-updated table and displays aircraft on Leaflet map.
console.log('[ADSB] Starting map client with HTMX...');

const center = [50, 8];
const zoom = 6;

const map = L.map('map').setView(center, zoom);
L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
  maxZoom: 19,
}).addTo(map);

// Track state per ICAO: { polyline, marker, coords[] }
const aircraft = new Map();
const MAX_TRACK_POINTS = 200;
const predictionStates = new Map(); // track which aircraft have prediction enabled

// Altitude-based color: blue (low) -> cyan -> green -> yellow -> red (high)
function colorByAltitude(alt) {
  const maxAlt = 45000; // max altitude in feet for color scale
  const clipped = Math.min(Math.max(alt, 0), maxAlt);
  const ratio = clipped / maxAlt; // 0 to 1
  // spectrum: blue (0) -> cyan (0.25) -> green (0.5) -> yellow (0.75) -> red (1)
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

function colorForId(id) {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h << 5) - h + id.charCodeAt(i);
  const hue = Math.abs(h) % 360;
  return `hsl(${hue},70%,45%)`;
}

// Handle HTMX swaps: when the table tbody is updated, process new aircraft data
document.addEventListener('htmx:afterSwap', function(event) {
  if (event.detail.target.id === 'adsb-debug-body') {
    // Read updated table rows and update map
    const tbody = document.getElementById('adsb-debug-body');
    const rows = tbody.querySelectorAll('tr');
    rows.forEach(row => {
      const cells = row.querySelectorAll('td');
      if (cells.length >= 5) {
        const icao = cells[0].textContent.trim();
        const lat = parseFloat(cells[1].textContent);
        const lon = parseFloat(cells[2].textContent);
        const alt = parseInt(cells[3].textContent);
        const speed = parseInt(cells[4].textContent);
        
        updateAircraft({
          icao,
          lat,
          lon,
          alt,
          speed,
          heading: 0,
          seen: new Date().toISOString()
        });
      }
    });
  }
});

// Update aircraft on map
function updateAircraft(data) {
  const id = data.icao;
  const lat = data.lat;
  const lon = data.lon;
  const alt = data.alt || 0;
  const speed = data.speed || 0;
  const heading = data.heading;
  const seen = new Date(data.seen);

  // Initialize entry if new
  if (!aircraft.has(id)) {
    aircraft.set(id, {
      coords: [],
      marker: null,
      polyline: null,
      segments: [],
      last: null,
      alt: 0,
      speed: 0,
      heading: 0
    });
  }

  const entry = aircraft.get(id);

  // Check if position changed
  if (entry.coords.length > 0) {
    const lastCoord = entry.coords[entry.coords.length - 1];
    if (lastCoord[0] === lat && lastCoord[1] === lon) {
      return; // No change
    }
  }

  // Add to coords history
  entry.coords.push([lat, lon]);
  if (entry.coords.length > MAX_TRACK_POINTS) {
    entry.coords.shift();
  }

  // Draw polyline on first update
  if (!entry.polyline) {
    entry.polyline = L.polyline(entry.coords, {
      color: colorForId(id),
      weight: 2,
      opacity: 0.6
    }).addTo(map);
  } else {
    entry.polyline.setLatLngs(entry.coords);
  }

  // Create or update marker
  const popup = `<strong>${id}</strong><br/>Alt: ${alt} ft<br/>Spd: ${speed} kt`;
  const newColor = colorByAltitude(alt);
  const svg = `<svg xmlns='http://www.w3.org/2000/svg' width='28' height='28' viewBox='0 0 24 24'><polygon points='12,2 4,20 12,15 20,20' fill='${newColor}'/></svg>`;
  const icon = L.divIcon({ className: 'plane-divicon', html: svg, iconSize: [28, 28] });

  if (!entry.marker) {
    entry.marker = L.marker([lat, lon], { icon: icon }).addTo(map);
    entry.marker.bindPopup(popup);
  } else {
    entry.marker.setLatLng([lat, lon]).setIcon(icon).getPopup().setContent(popup);
  }

  // Apply rotation if heading available
  if (heading) {
    applyRotation(entry.marker, heading);
  }

  entry.last = { alt, speed, heading, seen };
  entry.alt = alt;
  entry.speed = speed;
}

function applyRotation(marker, hdg) {
  try {
    const wrapper = marker.getElement();
    if (!wrapper) throw new Error('no wrapper');
    const svg = wrapper.querySelector && wrapper.querySelector('svg');
    if (svg) {
      svg.style.transformOrigin = '50% 50%';
      svg.style.transform = `rotate(${hdg}deg)`;
      return;
    }
  } catch (e) {}
  setTimeout(() => {
    try {
      const wrapper2 = marker.getElement();
      const svg2 = wrapper2 && wrapper2.querySelector && wrapper2.querySelector('svg');
      if (svg2) {
        svg2.style.transformOrigin = '50% 50%';
        svg2.style.transform = `rotate(${hdg}deg)`;
      }
    } catch (_) {}
  }, 120);
}

// Reapply rotations on map changes
map.on('zoomend moveend viewreset', () => {
  try {
    for (const [id, entry] of aircraft.entries()) {
      const hd = entry.last && entry.last.heading;
      if (typeof hd === 'number' && !isNaN(hd)) applyRotation(entry.marker, hd);
    }
  } catch (e) {}
});

// Add altitude legend
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

// Overlay buttons
document.getElementById('adsb-clear-tracks').addEventListener('click', () => {
  for (const entry of aircraft.values()) {
    if (entry.polyline) map.removeLayer(entry.polyline);
  }
  aircraft.clear();
});

document.getElementById('adsb-toggle-polys').addEventListener('click', () => {
  for (const entry of aircraft.values()) {
    if (entry.polyline) {
      if (map.hasLayer(entry.polyline)) {
        map.removeLayer(entry.polyline);
      } else {
        map.addLayer(entry.polyline);
      }
    }
  }
});

document.getElementById('adsb-hide-overlay').addEventListener('click', () => {
  document.getElementById('adsb-debug-overlay').style.display = 'none';
});

console.log('[ADSB] Map client ready. Waiting for HTMX table updates...');

// Altitude-based color: blue (low) -> cyan -> green -> yellow -> red (high)
// Altitude scale: 0 ft = blue, 45000 ft = red
function colorByAltitude(alt) {
  const maxAlt = 45000; // max altitude in feet for color scale
  const clipped = Math.min(Math.max(alt, 0), maxAlt);
  const ratio = clipped / maxAlt; // 0 to 1
  // spectrum: blue (0) -> cyan (0.25) -> green (0.5) -> yellow (0.75) -> red (1)
  let r, g, b;
  if (ratio < 0.25) {
    // blue to cyan
    const t = ratio / 0.25;
    r = 0;
    g = Math.round(255 * t);
    b = 255;
  } else if (ratio < 0.5) {
    // cyan to green
    const t = (ratio - 0.25) / 0.25;
    r = 0;
    g = 255;
    b = Math.round(255 * (1 - t));
  } else if (ratio < 0.75) {
    // green to yellow
    const t = (ratio - 0.5) / 0.25;
    r = Math.round(255 * t);
    g = 255;
    b = 0;
  } else {
    // yellow to red
    const t = (ratio - 0.75) / 0.25;
    r = 255;
    g = Math.round(255 * (1 - t));
    b = 0;
  }
  return `rgb(${r},${g},${b})`;
}

function colorForId(id) {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h << 5) - h + id.charCodeAt(i);
  const hue = Math.abs(h) % 360;
    return `hsl(${hue},70%,45%)`;
}

// Calculate prediction arrow: single point ahead based on speed, heading, and vertical rate
// Returns {endpoint: [lat, lon], control: [lat, lon]} for drawing a curved arrow
function calculatePredictionArrow(lat, lon, alt, speed, heading, verticalRate, coords) {
  if (!lat || !lon) {
    return null;
  }
  
  // If no heading, try to calculate from last two track points
  let calcHeading = heading;
  if ((heading === null || heading === undefined) && coords && coords.length >= 2) {
    const p1 = coords[coords.length - 2];
    const p2 = coords[coords.length - 1];
    calcHeading = bearing(p1[0], p1[1], p2[0], p2[1]);
  }
  
  if (calcHeading === null || calcHeading === undefined) {
    return null;
  }
  
  // Calculate distance based on speed: 20 seconds of flight visualized
  // Speed in knots -> convert to km in 20 seconds
  const timeSeconds = 20;
  const speed_mps = (speed || 0) / 1.943844; // convert knots to m/s
  const distance = speed_mps * timeSeconds / 1000; // distance in km
  
  // Endpoint: where the plane will be in 20 seconds
  const endpoint = destPoint(lat, lon, calcHeading, distance);
  
  // Control point: slightly offset perpendicular based on vertical rate
  // If climbing, curve upward (offset to the right), if descending, curve downward (offset to the left)
  const vrate_mps = (verticalRate || 0) / 196.85; // convert ft/min to m/s
  const curvature = Math.min(Math.max(vrate_mps / 5, -0.05), 0.05); // normalize curvature
  
  // Perpendicular offset (90 degrees from heading)
  const perpHeading = (calcHeading + 90) % 360;
  const controlDist = distance * 0.3 * curvature; // control point offset
  const control = destPoint(
    (lat + endpoint[0]) / 2,
    (lon + endpoint[1]) / 2,
    perpHeading,
    Math.abs(controlDist)
  );
  
  return {
    start: [lat, lon],
    endpoint: endpoint,
    control: control,
    heading: calcHeading,
    color: colorByAltitude(alt)
  };
}

// Draw prediction arrow on map
function drawPredictionArrow(id, entry, show) {
  try {
    // remove old arrow elements if exist
    if (entry.predictionArrow) {
      entry.predictionArrow.forEach(elem => {
        try { map.removeLayer(elem); } catch(e) {}
      });
      entry.predictionArrow = null;
    }
    if (entry.predictionArrowHead) {
      try { map.removeLayer(entry.predictionArrowHead); } catch(e) {}
      entry.predictionArrowHead = null;
    }
    
    if (!show || !entry.last) return;
    
    const lat = entry.coords[entry.coords.length - 1][0];
    const lon = entry.coords[entry.coords.length - 1][1];
    const alt = entry.last.alt || entry.alt;
    const speed = entry.last.speed || entry.speed;
    const heading = entry.last.heading;
    const verticalRate = entry.last.verticalRate || 0;
    
    const arrowData = calculatePredictionArrow(lat, lon, alt, speed, heading, verticalRate, entry.coords);
    if (!arrowData) {
      return;
    }
    
    // Draw curved arrow line
    const curve = L.polyline([arrowData.start, arrowData.control, arrowData.endpoint], {
      color: arrowData.color,
      weight: 3,
      opacity: 0.7,
      dashArray: '4, 4'
    }).addTo(map);
    
    // Draw arrowhead (two small lines forming a triangle)
    const headSize = 0.003; // degrees
    const angle1 = (arrowData.heading + 150) % 360;
    const angle2 = (arrowData.heading - 150) % 360;
    const arrowPoint1 = destPoint(arrowData.endpoint[0], arrowData.endpoint[1], angle1, headSize);
    const arrowPoint2 = destPoint(arrowData.endpoint[0], arrowData.endpoint[1], angle2, headSize);
    
    const arrowHead = L.polyline([arrowPoint1, arrowData.endpoint, arrowPoint2], {
      color: arrowData.color,
      weight: 3,
      opacity: 0.7
    }).addTo(map);
    
    entry.predictionArrow = [curve];
    entry.predictionArrowHead = arrowHead;
    
    console.log(`[ADSB] Prediction arrow drawn for ${id}`);
  } catch (e) {
    console.error('[ADSB] Prediction arrow error:', e);
  }
}

function getStreamUrl(){
  const host = window.location.hostname;
  return `http://${host}:8080/stream`;
}

const streamUrl = getStreamUrl();
console.log('[ADSB] Connecting to:', streamUrl);

const evtSource = new EventSource(streamUrl);

evtSource.onopen = () => console.log('[ADSB] EventSource connected');

evtSource.onmessage = function(e){
  try {
    const a = JSON.parse(e.data);
    if (!a.icao) return;
    const id = a.icao;
    const lat = Number(a.lat);
    const lon = Number(a.lon);
    if (!isFinite(lat) || !isFinite(lon)) return;

    const alt = parseInt(a.alt) || 0;
    const speed = parseInt(a.speed) || 0;
    const msgHeading = (typeof a.heading !== 'undefined') ? parseInt(a.heading) : null;
    const callsign = a.callsign || null;
    const squawk = a.squawk || null;
    const verticalRate = (typeof a.vertical_rate !== 'undefined') ? parseInt(a.vertical_rate) : null;
    const messages = (typeof a.messages !== 'undefined') ? parseInt(a.messages) : null;
    const rssi = (typeof a.rssi !== 'undefined') ? Number(a.rssi) : null;
    const onGround = (typeof a.on_ground !== 'undefined') ? Boolean(a.on_ground) : false;
    const source = a.source || null;
    const seen = a.seen ? new Date(a.seen) : new Date();

    const popup = `<div class="track-popup"><b>${id}</b><br/>Lat ${lat.toFixed(4)} &middot; Lon ${lon.toFixed(4)}<br/>Alt ${alt.toLocaleString()} ft &middot; Spd ${speed} kt<br/><small>${seen.toLocaleTimeString()}</small></div>`;

    if (!aircraft.has(id)) {
      const color = colorByAltitude(alt);
      // create an inline SVG icon to avoid emoji/encoding issues
      // inline SVG (no XML prolog) and class for targeting; avoids parsing/display artifacts
      const svg = `<svg xmlns='http://www.w3.org/2000/svg' width='28' height='28' viewBox='0 0 24 24' class='plane-svg'><polygon points='12,2 4,20 12,15 20,20' fill='${color}'/></svg>`;
      const icon = L.divIcon({ className: 'plane-divicon', html: svg, iconSize: [28,28] });
      const marker = L.marker([lat, lon], { icon, title: id, zIndexOffset: 1000 }).addTo(map).bindPopup(popup);
      // click selects aircraft for overlay details and centers it (preserving zoom level)
      marker.on('click', () => { 
        window.selectedAircraft = id;
        map.setView([lat, lon], map.getZoom());
      });

      // create entry: store coords, altitudes, and segment polylines
      // coords: array of [lat,lon], altitudes: array of altitudes at each point, segments: array of polylines (one per segment)
      const entry = { 
        marker: marker, 
        coords: [[lat,lon]], 
        altitudes: [alt],
        segments: [], // array of polylines, one for each track segment
        last: { alt, speed, heading: msgHeading, seen, callsign, squawk, verticalRate, messages, rssi, onGround, source }, 
        alt: alt, 
        speed: speed 
      };
      aircraft.set(id, entry);
      // apply rotation if heading present (safely)
      if (msgHeading !== null) applyRotation(marker, msgHeading);
      console.log(`[ADSB] New aircraft ${id}`);
    } else {
      const entry = aircraft.get(id);
      const prevCoord = entry.coords[entry.coords.length - 1];
      const prevAlt = entry.altitudes[entry.altitudes.length - 1];
      
      entry.coords.push([lat,lon]);
      entry.altitudes.push(alt);
      if (entry.coords.length > MAX_TRACK_POINTS) {
        entry.coords.shift();
        entry.altitudes.shift();
        // remove the oldest segment polyline
        if (entry.segments.length > 0) {
          const oldSegment = entry.segments.shift();
          map.removeLayer(oldSegment);
        }
      }
      
      // create a new segment polyline from previous point to current point
      // color is based on the PREVIOUS altitude (start of this segment)
      const segmentColor = colorByAltitude(prevAlt);
      const segmentPoly = L.polyline([prevCoord, [lat, lon]], { 
        color: segmentColor, 
        weight: 4, 
        opacity: 0.9 
      }).addTo(map);
      // put track segments behind markers
      if (segmentPoly.bringToBack) try { segmentPoly.bringToBack(); } catch (e) {}
      entry.segments.push(segmentPoly);
      
      // update last sample
      entry.last = { alt, speed, heading: msgHeading, seen, callsign, squawk, verticalRate, messages, rssi, onGround, source };
      entry.alt = alt; entry.speed = speed;
      
      // update marker position and color (color based on current altitude)
      const newColor = colorByAltitude(alt);
      const newSvg = `<svg xmlns='http://www.w3.org/2000/svg' width='28' height='28' viewBox='0 0 24 24' class='plane-svg'><polygon points='12,2 4,20 12,15 20,20' fill='${newColor}'/></svg>`;
      const newIcon = L.divIcon({ className: 'plane-divicon', html: newSvg, iconSize: [28,28] });
      entry.marker.setIcon(newIcon).setLatLng([lat, lon]).setPopupContent(popup);
      
      // rotate marker by heading if available
      const hd = (msgHeading !== null && !isNaN(msgHeading)) ? msgHeading : (entry.coords.length>1 ? Math.round(bearing(entry.coords[entry.coords.length-2][0], entry.coords[entry.coords.length-2][1], lat, lon)) : null);
      if (hd !== null) applyRotation(entry.marker, hd);
      
      // update prediction if enabled for this aircraft
      if (predictionStates.has(id)) {
        drawPredictionArrow(id, entry, true);
      }
    }
  } catch(err) {
    console.error('[ADSB] Parse error:', err);
  }
};

// compute bearing helper
function bearing(lat1, lon1, lat2, lon2) {
  const toRad = v => v * Math.PI / 180;
  const toDeg = v => v * 180 / Math.PI;
  const φ1 = toRad(lat1), φ2 = toRad(lat2);
  const Δλ = toRad(lon2 - lon1);
  const y = Math.sin(Δλ) * Math.cos(φ2);
  const x = Math.cos(φ1)*Math.sin(φ2) - Math.sin(φ1)*Math.cos(φ2)*Math.cos(Δλ);
  return (toDeg(Math.atan2(y,x)) + 360) % 360;
}

function applyRotation(marker, hdg) {
  // Prefer rotating the inner SVG so Leaflet's translate/scale on the wrapper isn't overwritten
  try {
    const wrapper = marker.getElement();
    if (!wrapper) throw new Error('no wrapper');
    const svg = wrapper.querySelector && wrapper.querySelector('svg.plane-svg');
    if (svg) {
      svg.style.transformOrigin = '50% 50%';
      svg.style.transform = `rotate(${hdg}deg)`;
      return;
    }
  } catch (e) {}
  // element may not exist yet (Leaflet may create it later), retry shortly
  setTimeout(() => {
    try {
      const wrapper2 = marker.getElement();
      const svg2 = wrapper2 && wrapper2.querySelector && wrapper2.querySelector('svg.plane-svg');
      if (svg2) {
        svg2.style.transformOrigin = '50% 50%';
        svg2.style.transform = `rotate(${hdg}deg)`;
      }
    } catch (_){ }
  }, 120);
}

// Reapply rotations after map view changes because Leaflet may recreate marker DOM nodes on zoom
map.on('zoomend moveend viewreset', () => {
  try {
    for (const [id, entry] of aircraft.entries()) {
      const hd = entry.last && entry.last.heading;
      if (typeof hd === 'number' && !isNaN(hd)) applyRotation(entry.marker, hd);
    }
  } catch (e) { /* ignore */ }
});

// Add altitude legend to the map with continuous color gradient
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
window.altitudeLegend = legend;
legend.addTo(map);

evtSource.onerror = function(e){
  console.error('[ADSB] EventSource error:', e);
  console.error('[ADSB] ReadyState:', evtSource.readyState, '(0=connecting, 1=open, 2=closed)');
};

// Notify if connection slow
setTimeout(() => {
  if (evtSource.readyState === 0) console.warn('[ADSB] Still connecting after 5 seconds...');
}, 5000);
