package doku

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

const timestampLayout = "2006-01-02T15:04:05Z"

// Sign builds DOKU's "Signature" header value.
//
//	component = "Client-Id:{id}\nRequest-Id:{rid}\nRequest-Timestamp:{ts}\nRequest-Target:{path}"
//	            + "\nDigest:{base64(sha256(body))}"   (only when there is a body)
//	signature = "HMACSHA256=" + base64(HMAC-SHA256(secretKey, component))
//
// The same algorithm signs requests to DOKU and DOKU's notifications to us.
func Sign(secretKey, clientID, requestID, timestamp, target string, body []byte) string {
	lines := []string{
		"Client-Id:" + clientID,
		"Request-Id:" + requestID,
		"Request-Timestamp:" + timestamp,
		"Request-Target:" + target,
	}
	if len(body) > 0 {
		digest := sha256.Sum256(body)
		lines = append(lines, "Digest:"+base64.StdEncoding.EncodeToString(digest[:]))
	}

	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(strings.Join(lines, "\n")))
	return "HMACSHA256=" + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
