// Web client runtime configuration.
//
// This file is static and is read by the browser at load time, so it can be
// edited per-deployment without rebuilding anything. `apiBase` is the origin of
// the Murmur AppView API (HTTP + WebSocket). Leave it as "" to address the API
// on the same origin that served this page (the legacy behavior, e.g. if the UI
// is ever fronted by a reverse proxy that also routes /api and /ws).
window.MURMUR_CONFIG = {
  apiBase: "http://localhost:2472",
};
