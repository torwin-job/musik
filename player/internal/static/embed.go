package static

import "embed"

//go:embed index.html app.js style.css fonts.css themes/*.css fonts/*.woff2 icons/*.png
var FS embed.FS
