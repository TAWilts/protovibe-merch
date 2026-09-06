// Package qrimage renders payloads as inline QR code images.
//
// The codes are produced server-side and handed out as data URIs so no page
// has to pull a rendering library over the network — the app must keep working
// at a merch stand with no usable connection.
package qrimage

import (
	"bytes"
	"encoding/base64"

	qrcode "github.com/skip2/go-qrcode"
)

// DataURI encodes a payload as a PNG data URI with the given edge length.
//
// Medium error correction is the right trade-off for a phone screen held at a
// merch table: it survives a fingerprint or a glare spot without inflating the
// code so much that it stops scanning from arm's length.
func DataURI(payload string, size int) (string, error) {
	png, err := qrcode.Encode(payload, qrcode.Medium, size)
	if err != nil {
		return "", err
	}
	var buffer bytes.Buffer
	buffer.WriteString("data:image/png;base64,")
	buffer.WriteString(base64.StdEncoding.EncodeToString(png))
	return buffer.String(), nil
}
