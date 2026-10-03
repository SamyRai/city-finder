package app

import (
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// etagApps returns fiber's built-in ETag (the previous production config)
// and the ETag middleware, over identical routes.
func etagApps() (builtin, ours *fiber.App) {
	routes := func(a *fiber.App) {
		a.Get("/json", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"status": "ok"}) })
		a.Get("/text/:n", func(c *fiber.Ctx) error { return c.SendString("payload-" + c.Params("n")) })
		a.Get("/empty", func(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusOK) })
		a.Get("/created", func(c *fiber.Ctx) error { return c.Status(201).SendString("x") })
		a.Get("/bad", func(c *fiber.Ctx) error { return c.Status(400).SendString("nope") })
		a.Get("/err", func(c *fiber.Ctx) error { return fiber.ErrNotFound })
	}
	builtin = fiber.New(fiber.Config{ETag: true})
	routes(builtin)
	ours = fiber.New()
	ours.Use(ETag())
	routes(ours)
	return builtin, ours
}

type etagResult struct {
	status int
	etag   string
	body   string
}

func etagDo(t *testing.T, a *fiber.App, path, ifNoneMatch string) etagResult {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	resp, err := a.Test(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return etagResult{resp.StatusCode, resp.Header.Get("ETag"), string(body)}
}

// TestETagMatchesFiberBuiltin pins that the middleware is a drop-in for
// fiber's Config.ETag: identical status, ETag header and body for fresh
// requests, matching/mismatching strong and weak If-None-Match values, tag
// lists, non-200 responses, empty bodies, errors and unrouted paths.
func TestETagMatchesFiberBuiltin(t *testing.T) {
	builtin, ours := etagApps()
	paths := []string{"/json", "/text/1", "/text/22", "/empty", "/created", "/bad", "/err", "/missing"}
	for _, p := range paths {
		fresh := etagDo(t, builtin, p, "")
		assert.Equal(t, fresh, etagDo(t, ours, p, ""), "%s fresh", p)

		conds := []string{"*", `"0-0"`, "W/" + fresh.etag, `"1-2", ` + fresh.etag, "W/x"}
		if fresh.etag != "" {
			conds = append(conds, fresh.etag, "W/"+fresh.etag[2:])
		}
		for _, inm := range conds {
			assert.Equal(t, etagDo(t, builtin, p, inm), etagDo(t, ours, p, inm), "%s If-None-Match %q", p, inm)
		}
	}
	// Sanity: the comparison above exercised a real 304.
	tag := etagDo(t, ours, "/json", "").etag
	require.NotEmpty(t, tag)
	assert.Equal(t, 304, etagDo(t, ours, "/json", tag).status)
}

// TestETagConditionalQuirks pins two lax behaviours inherited from fiber's
// built-in ETag (product decision, E10): RFC 9110 says `If-None-Match: *`
// matches any current representation (304), but it is answered 200 here, and
// the tag is matched as a substring, so a tag wrapped in garbage still yields
// 304. Changing either must move TestETagMatchesFiberBuiltin with it.
func TestETagConditionalQuirks(t *testing.T) {
	_, ours := etagApps()
	tag := etagDo(t, ours, "/json", "").etag
	require.NotEmpty(t, tag)

	star := etagDo(t, ours, "/json", "*")
	assert.Equal(t, 200, star.status, "If-None-Match: * is not treated as a match")
	assert.Equal(t, tag, star.etag)

	assert.Equal(t, 304, etagDo(t, ours, "/json", "garbage"+tag+"garbage").status,
		"a tag embedded in other text still matches")
	assert.Equal(t, 200, etagDo(t, ours, "/json", `"0-0"`).status, "a different tag does not match")
}
