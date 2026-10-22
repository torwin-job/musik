package static

import "embed"

//go:embed index.html mozgolika.html rainedev.html rainedev.css rainedev.js app.js style.css redesign.css fonts.css themes/*.css fonts/*.woff2 icons/*.png icons/*.svg
var FS embed.FS
