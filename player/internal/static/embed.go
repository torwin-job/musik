package static

import "embed"

//go:embed index.html app.js style.css
var FS embed.FS
