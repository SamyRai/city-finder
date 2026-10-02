package app

import (
	"hash/crc32"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
)

// etagTable is the CRC-32 table for the polynomial fiber's built-in ETag
// uses (0xD5828281). Fiber rebuilds this 256-entry table on EVERY response
// (crc32.MakeTable only caches the IEEE table) and formats the tag with
// fmt.Sprintf; building it once is what makes this middleware cheap.
var etagTable = crc32.MakeTable(0xD5828281)

// ETag returns middleware that produces exactly the strong ETag behavior of
// fiber's deprecated Config.ETag — same tag value ("<len>-<crc32>"), same
// If-None-Match matching, same 304 — without its per-response table build
// and Sprintf (measured ~4–5 µs and ~1 KB of garbage per response). Like
// fiber, it only tags successful (200) non-empty responses of requests that
// reached a handler without error.
func ETag() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if err := c.Next(); err != nil {
			return err
		}
		resp := c.Response()
		if resp.StatusCode() != fiber.StatusOK {
			return nil
		}
		body := resp.Body()
		if len(body) == 0 {
			return nil
		}
		buf := make([]byte, 0, 32)
		buf = append(buf, '"')
		buf = strconv.AppendInt(buf, int64(len(body)), 10)
		buf = append(buf, '-')
		buf = strconv.AppendUint(buf, uint64(crc32.Checksum(body, etagTable)), 10)
		buf = append(buf, '"')
		etag := string(buf)

		clientEtag := c.Get(fiber.HeaderIfNoneMatch)
		if strings.HasPrefix(clientEtag, "W/") {
			// fiber compares a weak client tag against the tag and against
			// the tag minus its first two bytes; mirrored verbatim.
			if clientEtag[2:] == etag || clientEtag[2:] == etag[2:] {
				return notModified(c)
			}
			c.Set(fiber.HeaderETag, etag)
			return nil
		}
		if strings.Contains(clientEtag, etag) {
			return notModified(c)
		}
		c.Set(fiber.HeaderETag, etag)
		return nil
	}
}

func notModified(c *fiber.Ctx) error {
	c.Status(fiber.StatusNotModified)
	c.Response().ResetBody()
	return nil
}
