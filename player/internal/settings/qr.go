package settings

import (
	"fmt"
	"net/url"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// ConnectPayload is what the phone apps scan to add this server:
//
//	musik://connect?url=<base url>&token=<api token>
//
// The iOS and Android apps parse it; a QR with a plain http(s) URL only
// fills the address. The token is omitted when the server has none.
func ConnectPayload(baseURL, token string) string {
	q := url.Values{}
	q.Set("url", strings.TrimRight(baseURL, "/"))
	if token != "" {
		q.Set("token", token)
	}
	return "musik://connect?" + q.Encode()
}

// QRSVG renders content as a black-on-white SVG (scanners need dark modules
// on a light background regardless of the page theme), with the quiet zone.
func QRSVG(content string) (string, error) {
	code, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return "", err
	}
	bitmap := code.Bitmap()
	n := len(bitmap)
	var path strings.Builder
	for y, row := range bitmap {
		for x := 0; x < len(row); {
			if !row[x] {
				x++
				continue
			}
			start := x
			for x < len(row) && row[x] {
				x++
			}
			fmt.Fprintf(&path, "M%d %dh%dv1h-%dz", start, y, x-start, x-start)
		}
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`+
		`<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="%s"/></svg>`,
		n, n, n, n, path.String()), nil
}
