package fake

import (
	"context"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFakeOplogMethods(t *testing.T) {
	ctx := context.Background()

	t.Run("GetOplogSizeMB returns configured size", func(t *testing.T) {
		c := &fakeMongoClient{OplogSizeMB: 990}
		size, err := c.GetOplogSizeMB(ctx)
		require.NoError(t, err)
		assert.Equal(t, float64(990), size)
	})

	t.Run("GetOplogSizeMB returns configured error", func(t *testing.T) {
		c := &fakeMongoClient{GetOplogSizeMBErr: errors.New("boom")}
		_, err := c.GetOplogSizeMB(ctx)
		assert.Error(t, err)
	})

	t.Run("ResizeOplog updates size and counts calls", func(t *testing.T) {
		c := &fakeMongoClient{OplogSizeMB: 990}
		require.NoError(t, c.ResizeOplog(ctx, 2000))
		assert.Equal(t, float64(2000), c.OplogSizeMB)
		assert.Equal(t, 1, c.ResizeOplogCalls)

		size, err := c.GetOplogSizeMB(ctx)
		require.NoError(t, err)
		assert.Equal(t, float64(2000), size)
	})

	t.Run("ResizeOplog error does not update size", func(t *testing.T) {
		c := &fakeMongoClient{OplogSizeMB: 990, ResizeOplogErr: errors.New("crash")}
		err := c.ResizeOplog(ctx, 2000)
		assert.Error(t, err)
		assert.Equal(t, float64(990), c.OplogSizeMB)
		assert.Equal(t, 1, c.ResizeOplogCalls)
	})

	t.Run("CompactOplog counts calls and returns configured error", func(t *testing.T) {
		c := &fakeMongoClient{CompactOplogErr: errors.New("busy")}
		err := c.CompactOplog(ctx)
		assert.Error(t, err)
		assert.Equal(t, 1, c.CompactOplogCalls)
	})
}
