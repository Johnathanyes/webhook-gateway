package sourcedef

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Sign produces the headers a real provider would send for body, computed
// against secret using def's verification scheme — Verify's inverse. It
// backs the test-event generator, so a generated test event exercises
// the real verification path rather than bypassing it.
func Sign(def Definition, body, secret []byte) (http.Header, error) {
	headers := make(http.Header, len(def.SampleHeaders)+1)
	for k, v := range def.SampleHeaders {
		headers.Set(k, v)
	}

	switch def.Verification.Type {
	case "none":
		return headers, nil
	case "hmac":
		return signHMAC(def.Verification, body, secret, headers)
	case "api_key":
		headers.Set(def.Verification.SignatureHeader, def.Verification.SignaturePrefix+string(secret))
		return headers, nil
	case "basic_auth":
		headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString(secret))
		return headers, nil
	default:
		return nil, fmt.Errorf("unknown verification type %q", def.Verification.Type)
	}
}

func signHMAC(v Verification, body, secret []byte, headers http.Header) (http.Header, error) {
	payload := body
	if v.Timestamp != nil {
		ts := time.Now().Unix()
		payload = []byte(strconv.FormatInt(ts, 10) + "." + string(body))
		sig, err := encodeSignature(computeHMAC(v.Algorithm, secret, payload), v.Encoding)
		if err != nil {
			return nil, err
		}
		headers.Set(v.SignatureHeader, fmt.Sprintf("%s=%d,%s=%s", v.Timestamp.TimestampField, ts, v.Timestamp.SignatureField, sig))
		return headers, nil
	}

	sig, err := encodeSignature(computeHMAC(v.Algorithm, secret, payload), v.Encoding)
	if err != nil {
		return nil, err
	}
	headers.Set(v.SignatureHeader, v.SignaturePrefix+sig)
	return headers, nil
}

func encodeSignature(sig []byte, encoding string) (string, error) {
	switch encoding {
	case "hex":
		return hex.EncodeToString(sig), nil
	case "base64":
		return base64.StdEncoding.EncodeToString(sig), nil
	default:
		return "", fmt.Errorf("unknown encoding %q", encoding)
	}
}
