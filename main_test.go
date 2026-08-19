package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolvePublicOrigin(t *testing.T) {
	t.Run("Railway domain", func(t *testing.T) {
		t.Setenv("PUBLIC_URL", "")
		t.Setenv("RAILWAY_PUBLIC_DOMAIN", "readonly-production.up.railway.app")
		assert.Equal(t, "https://readonly-production.up.railway.app", resolvePublicOrigin(":8080"))
	})

	t.Run("explicit public URL wins", func(t *testing.T) {
		t.Setenv("PUBLIC_URL", "https://mcp.example.com/")
		t.Setenv("RAILWAY_PUBLIC_DOMAIN", "ignored.up.railway.app")
		assert.Equal(t, "https://mcp.example.com", resolvePublicOrigin(":8080"))
	})

	t.Run("local fallback", func(t *testing.T) {
		t.Setenv("PUBLIC_URL", "")
		t.Setenv("RAILWAY_PUBLIC_DOMAIN", "")
		assert.Equal(t, "http://localhost:18060", resolvePublicOrigin(":18060"))
	})
}
