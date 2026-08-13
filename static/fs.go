package static

import "embed"

// Content holds the embedded admin UI assets.
//
//go:embed index.html app.js style.css timeline.js state.js core.js channels.js epgs.js relays.js viewer.js settings.js
var Content embed.FS
