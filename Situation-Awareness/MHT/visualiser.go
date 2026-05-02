package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// startVisualiser spins up the small HTTP server exposing the particle
// visualiser. It is intentionally dependency-free (stdlib only).
//
// Endpoints:
//
//	GET  /                        → embedded HTML viewer
//	GET  /api/tracks              → JSON list of TrackSummary
//	GET  /api/track/{id}          → JSON TrackDetail (full particle cloud)
//	POST /api/select?id=…         → store the operator's selection
//	GET  /api/selected            → currently selected ID (or "")
//	GET  /api/stream              → text/event-stream — every 500 ms
//	                                 emits the selected track's detail.
func startVisualiser(ctx context.Context, addr string, t *Tracker) {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/tracks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, t.ListTracks())
	})

	mux.HandleFunc("/api/track/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/track/")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		d, ok := t.GetTrack(id)
		if !ok {
			http.Error(w, "no such track", http.StatusNotFound)
			return
		}
		writeJSON(w, d)
	})

	mux.HandleFunc("/api/all", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, t.ListAllDetails())
	})

	mux.HandleFunc("/api/select", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if !t.SetSelected(id) {
			http.Error(w, "no such track", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/api/selected", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"id": t.GetSelected()})
	})

	mux.HandleFunc("/api/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				// Always emit the full set so every track can be drawn
				// in its own colour, then the selected track on top.
				all := t.ListAllDetails()
				if ab, err := json.Marshal(all); err == nil {
					fmt.Fprintf(w, "event: all\ndata: %s\n\n", ab)
				}
				id := t.GetSelected()
				if id == "" {
					fmt.Fprintf(w, "event: empty\ndata: {}\n\n")
					fl.Flush()
					continue
				}
				d, ok := t.GetTrack(id)
				if !ok {
					fmt.Fprintf(w, "event: gone\ndata: {\"id\":\"%s\"}\n\n", id)
					fl.Flush()
					continue
				}
				b, _ := json.Marshal(d)
				fmt.Fprintf(w, "event: detail\ndata: %s\n\n", b)
				fl.Flush()
			}
		}
	})

	// Tiny inline SVG favicon — avoids the noisy 404 in DevTools.
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16">` +
			`<rect width="16" height="16" fill="#0b0f14"/>` +
			`<circle cx="8" cy="8" r="3" fill="#22d3ee"/>` +
			`<circle cx="3" cy="4" r="1" fill="#fbbf24"/>` +
			`<circle cx="13" cy="5" r="1" fill="#fbbf24"/>` +
			`<circle cx="4" cy="12" r="1" fill="#fbbf24"/>` +
			`<circle cx="12" cy="12" r="1" fill="#fbbf24"/>` +
			`</svg>`))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		// Never cache the shell HTML — otherwise older buggy versions
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(visualiserHTML))
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           recoverMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout — the SSE stream is intentionally long-lived.
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Printf("df-tracker: visualiser on http://%s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("df-tracker: visualiser stopped: %v", err)
	}
}

// recoverMiddleware turns any handler panic into a 500 JSON response
// instead of letting net/http close the TCP connection silently
// (which surfaces in the browser as ERR_EMPTY_RESPONSE).
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("df-tracker: panic in %s %s: %v", r.Method, r.URL.Path, rec)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"internal"}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(payload)
}

const visualiserHTML = `<!doctype html>
<html lang="de">
<head>
<meta charset="utf-8">
<title>DF-Tracker — Particle Filter Visualiser</title>
<link href="https://unpkg.com/maplibre-gl@3.6.2/dist/maplibre-gl.css" rel="stylesheet">
<style>
  html, body { margin:0; padding:0; height:100%; background:#0b0f14; color:#e6edf3;
    font-family: ui-sans-serif, system-ui, sans-serif; }
  #app { display:grid; grid-template-columns: 320px 1fr; height:100%; }
  #side { background:#0e141b; border-right:1px solid #1f2630; overflow:auto; }
  #side header { padding:12px 14px; border-bottom:1px solid #1f2630; }
  #side h1 { margin:0; font-size:14px; font-weight:600; letter-spacing:.04em; text-transform:uppercase; color:#7dd3fc; }
  #side small { color:#8b949e; }
  #nav { display:flex; gap:6px; margin-top:8px; }
  #nav button { flex:1; background:#161c24; color:#e6edf3; border:1px solid #1f2630; border-radius:4px;
    padding:6px 8px; font-size:12px; cursor:pointer; font-family:inherit; }
  #nav button:hover:not(:disabled) { background:#1d2530; border-color:#7dd3fc; }
  #nav button:disabled { opacity:.4; cursor:not-allowed; }
  #nav .count { flex:0 0 auto; padding:6px 4px; font-size:11px; color:#8b949e; align-self:center; }
  #follow { display:flex; align-items:center; gap:6px; margin-top:6px; font-size:11px; color:#8b949e; }
  #follow input { accent-color:#7dd3fc; }
  #tracks { list-style:none; margin:0; padding:0; }
  #tracks li { padding:10px 14px; border-bottom:1px solid #161c24; cursor:pointer; }
  #tracks li:hover { background:#11181f; }
  #tracks li.selected { background:#13334a; border-left:3px solid #7dd3fc; padding-left:11px; }
  #tracks .id { font-family: ui-monospace, monospace; color:#7dd3fc; font-size:13px; }
  #tracks .meta { color:#8b949e; font-size:11px; margin-top:2px; }
  #tracks .conf { color:#22d3ee; }
  #tracks .pending { color:#f59e0b; }
  #map { height:100%; }
  #legend { position:absolute; right:10px; top:10px; background:#0e141bcc; padding:10px 12px;
    border:1px solid #1f2630; border-radius:6px; font-size:12px; backdrop-filter: blur(4px); }
  #legend h2 { margin:0 0 6px; font-size:11px; text-transform:uppercase; color:#7dd3fc; letter-spacing:.05em; }
  .row { display:flex; align-items:center; gap:8px; margin:3px 0; }
  .swatch { width:12px; height:12px; border-radius:50%; }
  .swatch.particle { background: linear-gradient(90deg,#f87171,#fbbf24,#34d399,#22d3ee,#a78bfa); opacity:.85; }
  .swatch.mean { background:#ffffff; border:2px solid #22d3ee; }
  .swatch.sensor { background:#a78bfa; }
  .swatch.bearing { background:transparent; border:2px dashed #f87171; border-radius:0; }
  .dot { width:10px; height:10px; border-radius:50%; display:inline-block; margin-right:6px;
    vertical-align:middle; border:1px solid #00000066; }
</style>
</head>
<body>
<div id="app">
  <aside id="side">
    <header>
      <h1>DF-Tracker</h1>
      <small id="status">verbinde…</small>
      <div id="nav">
        <button id="prev" title="Vorheriges Ziel (←)">◀ Vor</button>
        <span class="count" id="count">0/0</span>
        <button id="next" title="Nächstes Ziel (→)">Zurück ▶</button>
      </div>
      <label id="follow"><input type="checkbox" id="followBox" checked> Karte folgt Auswahl</label>
    </header>
    <ul id="tracks"></ul>
  </aside>
  <main style="position:relative">
    <div id="map"></div>
    <div id="legend">
      <h2>Legende</h2>
      <div class="row"><span class="swatch particle"></span>Partikel je Track (eigene Farbe)</div>
      <div class="row"><span class="swatch mean"></span>Gewichteter Mittelpunkt</div>
      <div class="row"><span class="swatch sensor"></span>Letzter Sensor (Auswahl)</div>
      <div class="row"><span class="swatch bearing"></span>Letzte Peilung (Auswahl)</div>
    </div>
  </main>
</div>
<script src="https://unpkg.com/maplibre-gl@3.6.2/dist/maplibre-gl.js"></script>
<script>
const map = new maplibregl.Map({
  container: 'map',
  style: 'https://basemaps.cartocdn.com/gl/dark-matter-gl-style/style.json',
  center: [18, 56], zoom: 5,
});

const empty = () => ({ type:'FeatureCollection', features: [] });
let selectedId = null;
let trackOrder = [];   // ids sorted as displayed in the sidebar
let didFitOnce = false;
let followSelection = true;

map.on('load', () => {
  // One source for ALL particles across all tracks (coloured per track),
  // one for ALL means (labelled with track id), plus dedicated sources
  // for the currently selected track's last sensor + bearing line.
  map.addSource('particles', { type:'geojson', data: empty() });
  map.addSource('mean',      { type:'geojson', data: empty() });
  map.addSource('sensor',    { type:'geojson', data: empty() });
  map.addSource('bearing',   { type:'geojson', data: empty() });

  // Particle dots — colour from per-feature 'color', radius from weight.
  // Selected track is rendered with full opacity and slightly larger.
  map.addLayer({
    id: 'particles', source: 'particles', type: 'circle',
    paint: {
      'circle-color': ['get', 'color'],
      'circle-opacity': ['case', ['get', 'sel'], 0.85, 0.45],
      'circle-radius': [
        'interpolate', ['linear'], ['get', 'w'],
        0,   1.0,
        0.01, 2.5,
        0.05, 4.5,
        0.2,  6.5
      ]
    }
  });
  // Mean: filled circle in the track's colour, white halo.
  map.addLayer({
    id: 'mean', source: 'mean', type: 'circle',
    paint: {
      'circle-color': ['get', 'color'],
      'circle-stroke-color': '#ffffff',
      'circle-stroke-width': ['case', ['get', 'sel'], 3, 2],
      'circle-radius':       ['case', ['get', 'sel'], 9, 6]
    }
  });
  map.addLayer({
    id: 'mean-label', source: 'mean', type: 'symbol',
    layout: {
      'text-field': ['get', 'id'],
      'text-size': 11,
      'text-offset': [0, 1.2],
      'text-anchor': 'top',
      'text-allow-overlap': false,
    },
    paint: {
      'text-color': '#e6edf3',
      'text-halo-color': '#0b0f14',
      'text-halo-width': 1.5,
    }
  });
  map.addLayer({
    id: 'sensor', source: 'sensor', type: 'circle',
    paint: { 'circle-color': '#a78bfa', 'circle-radius': 5,
             'circle-stroke-color':'#fff', 'circle-stroke-width':1 }
  });
  map.addLayer({
    id: 'bearing', source: 'bearing', type: 'line',
    paint: { 'line-color': '#f87171', 'line-width': 2, 'line-dasharray': [2,2] }
  });

  startStream();
  pollTrackList();
  setInterval(pollTrackList, 2000);
});

// Stable HSL colour per track-id — independent of order, so each track
// keeps the same colour across reloads & list re-orderings.
function colorFor(id) {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0;
  const hue = h % 360;
  return 'hsl(' + hue + ', 80%, 60%)';
}

// Sidebar polling is independent of the map being ready. Kicked off
// twice (immediately + once the map renders) so a tile-load hiccup
// can never leave the sidebar stuck on the initial 'verbinde…' label.
pollTrackList();
setInterval(pollTrackList, 2000);

async function pollTrackList() {
  try {
    const r1 = await fetch('/api/tracks',   { cache: 'no-store' });
    if (!r1.ok) throw new Error('tracks ' + r1.status);
    const tracks = await r1.json();
    const r2 = await fetch('/api/selected', { cache: 'no-store' });
    if (!r2.ok) throw new Error('selected ' + r2.status);
    const sel = await r2.json();
    selectedId = sel.id;
    trackOrder = tracks.map(t => t.id);
    renderTrackList(tracks);
    updateNav(tracks);
    document.getElementById('status').textContent =
      tracks.length + ' aktive Tracks';
  } catch (e) {
    console.warn('pollTrackList failed:', e);
    document.getElementById('status').textContent =
      'Verbindung… (' + (e.message || 'fehler') + ')';
  }
}

function updateNav(tracks) {
  const idx = trackOrder.indexOf(selectedId);
  document.getElementById('count').textContent =
    (idx < 0 ? '–' : (idx + 1)) + '/' + trackOrder.length;
  document.getElementById('prev').disabled = trackOrder.length === 0;
  document.getElementById('next').disabled = trackOrder.length === 0;
}

function step(delta) {
  if (!trackOrder.length) return;
  let idx = trackOrder.indexOf(selectedId);
  if (idx < 0) idx = 0;
  else idx = (idx + delta + trackOrder.length) % trackOrder.length;
  selectTrack(trackOrder[idx]);
}

document.getElementById('prev').addEventListener('click', () => step(-1));
document.getElementById('next').addEventListener('click', () => step(+1));
document.getElementById('followBox').addEventListener('change', (e) => {
  followSelection = e.target.checked;
  if (followSelection) didFitOnce = false;
});

window.addEventListener('keydown', (e) => {
  if (e.target && /input|textarea|select/i.test(e.target.tagName)) return;
  if (e.key === 'ArrowLeft' || e.key === 'ArrowUp')   { e.preventDefault(); step(-1); }
  if (e.key === 'ArrowRight' || e.key === 'ArrowDown'){ e.preventDefault(); step(+1); }
});

function renderTrackList(tracks) {
  const ul = document.getElementById('tracks');
  ul.innerHTML = '';
  tracks.forEach(t => {
    const li = document.createElement('li');
    li.dataset.id = t.id;
    if (t.id === selectedId) li.classList.add('selected');
    const stat = t.confirmed
      ? '<span class="conf">CONFIRMED</span>'
      : '<span class="pending">tentative</span>';
    li.innerHTML =
      '<div class="id"><span class="dot" style="background:' + colorFor(t.id) + '"></span>' + t.id + '</div>' +
      '<div class="meta">' + stat
        + ' · ' + t.numSensors + ' Sensor' + (t.numSensors !== 1 ? 'en' : '')
        + ' · σ=' + (t.stdDevM/1000).toFixed(1) + ' km'
        + ' · ' + t.numUpdates + ' Updates</div>';
    li.onclick = () => selectTrack(t.id);
    ul.appendChild(li);
  });
}

async function selectTrack(id) {
  selectedId = id;
  didFitOnce = false;
  // Optimistic re-render so the highlight moves immediately, even
  // before the next /api/tracks poll returns.
  document.querySelectorAll('#tracks li').forEach(li => {
    li.classList.toggle('selected', li.dataset.id === id);
    if (li.dataset.id === id) li.scrollIntoView({ block: 'nearest' });
  });
  try {
    await fetch('/api/select?id=' + encodeURIComponent(id), { method: 'POST' });
    // Pull the new detail right away — don't wait for the 500ms SSE tick.
    const d = await fetch('/api/track/' + encodeURIComponent(id)).then(r => r.json());
    renderSelected(d);
  } catch (_) { /* ignored — next SSE tick will catch up */ }
  pollTrackList();
}

function startStream() {
  const es = new EventSource('/api/stream');
  es.addEventListener('all', (ev) => {
    try { renderAll(JSON.parse(ev.data) || []); } catch(_) {}
  });
  es.addEventListener('detail', (ev) => {
    const d = JSON.parse(ev.data);
    renderSelected(d);
  });
  es.addEventListener('empty', () => clearSelected());
  es.addEventListener('gone',  () => clearSelected());
  es.onerror = () => {
    setTimeout(startStream, 2000);
    es.close();
  };
}

function clearSelected() {
  ['sensor','bearing'].forEach(k => {
    map.getSource(k)?.setData(empty());
  });
}

// renderAll draws particles + means for every live track. Each track
// keeps its own stable colour; the currently selected track is rendered
// on top with full opacity.
function renderAll(list) {
  const partFeats = [];
  const meanFeats = [];
  for (const d of list) {
    if (!d || !d.particles) continue;
    const c = colorFor(d.id);
    const sel = d.id === selectedId;
    for (const p of d.particles) {
      partFeats.push({
        type: 'Feature',
        geometry: { type: 'Point', coordinates: [p[1], p[0]] },
        properties: { w: p[2], color: c, sel: sel, id: d.id }
      });
    }
    if (d.mean && (d.mean[0] || d.mean[1])) {
      meanFeats.push({
        type: 'Feature',
        geometry: { type: 'Point', coordinates: [d.mean[1], d.mean[0]] },
        properties: { id: d.id, color: c, sel: sel }
      });
    }
  }
  // Move the selected track's features last so they paint on top.
  partFeats.sort((a, b) => (a.properties.sel ? 1 : 0) - (b.properties.sel ? 1 : 0));
  meanFeats.sort((a, b) => (a.properties.sel ? 1 : 0) - (b.properties.sel ? 1 : 0));
  map.getSource('particles')?.setData({ type: 'FeatureCollection', features: partFeats });
  map.getSource('mean')?.setData({ type: 'FeatureCollection', features: meanFeats });

  // First-time fit: zoom to show all tracks together.
  if (!didFitOnce && partFeats.length > 0) {
    const lats = partFeats.map(f => f.geometry.coordinates[1]);
    const lons = partFeats.map(f => f.geometry.coordinates[0]);
    const bb = [
      [Math.min(...lons), Math.min(...lats)],
      [Math.max(...lons), Math.max(...lats)],
    ];
    map.fitBounds(bb, { padding: 80, maxZoom: 9, duration: 600 });
    didFitOnce = true;
  }
}

// renderSelected only updates the sensor + bearing overlay for the
// currently chosen track. Particle / mean layers are driven by renderAll.
function renderSelected(d) {
  if (!d) { clearSelected(); return; }
  if (d.lastSensor && (d.lastSensor[0] || d.lastSensor[1])) {
    map.getSource('sensor').setData({
      type: 'FeatureCollection',
      features: [{
        type: 'Feature',
        geometry: { type: 'Point', coordinates: [d.lastSensor[1], d.lastSensor[0]] },
      }]
    });

    // bearing line: extend ~250 km in the observed direction
    const br = d.lastBearing * Math.PI / 180;
    const R = 6378137;
    const dist = 250000;
    const lat1 = d.lastSensor[0] * Math.PI / 180;
    const lon1 = d.lastSensor[1] * Math.PI / 180;
    const dR = dist / R;
    const lat2 = Math.asin(Math.sin(lat1)*Math.cos(dR) + Math.cos(lat1)*Math.sin(dR)*Math.cos(br));
    const lon2 = lon1 + Math.atan2(Math.sin(br)*Math.sin(dR)*Math.cos(lat1),
                                   Math.cos(dR) - Math.sin(lat1)*Math.sin(lat2));
    map.getSource('bearing').setData({
      type: 'FeatureCollection',
      features: [{
        type: 'Feature',
        geometry: {
          type: 'LineString',
          coordinates: [
            [d.lastSensor[1], d.lastSensor[0]],
            [lon2 * 180/Math.PI, lat2 * 180/Math.PI],
          ]
        }
      }]
    });
  } else {
    clearSelected();
  }

  // Optional auto-pan to the selected track when follow-mode is on.
  if (followSelection && !didFitOnce && d.particles && d.particles.length > 0) {
    const lats = d.particles.map(p => p[0]);
    const lons = d.particles.map(p => p[1]);
    const bb = [
      [Math.min(...lons), Math.min(...lats)],
      [Math.max(...lons), Math.max(...lats)],
    ];
    map.fitBounds(bb, { padding: 80, maxZoom: 10, duration: 600 });
    didFitOnce = true;
  }
}
</script>
</body>
</html>
`
