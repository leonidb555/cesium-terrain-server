package sqlite

import (
	"database/sql"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/geo-data/cesium-terrain-server/stores"
	_ "modernc.org/sqlite"
)

type CacheKey struct {
	Tileset string
	Z       uint64
	X       uint64
	Y       uint64
}

type Store struct {
	db *sql.DB

	tileStmt   *sql.Stmt
	layerStmt  *sql.Stmt
	statusStmt *sql.Stmt

	cache *lru.Cache[CacheKey, []byte]
}

func New(path string, cacheSize int) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(4)

	if _, err := db.Exec("PRAGMA query_only = ON"); err != nil {
		return nil, err
	}

	if _, err := db.Exec("PRAGMA mmap_size = 1073741824"); err != nil {
		return nil, err
	}

	tileStmt, err := db.Prepare(`
		SELECT data
		FROM terrain_tiles
		WHERE tileset = ? AND z = ? AND x = ? AND y = ?
	`)
	if err != nil {
		return nil, err
	}

	layerStmt, err := db.Prepare(`
		SELECT layer_json
		FROM tilesets
		WHERE name = ?
	`)
	if err != nil {
		return nil, err
	}

	statusStmt, err := db.Prepare(`
		SELECT 1
		FROM tilesets
		WHERE name = ?
		LIMIT 1
	`)
	if err != nil {
		return nil, err
	}

	var cache *lru.Cache[CacheKey, []byte]
	if cacheSize > 0 {
		cache, err = lru.New[CacheKey, []byte](cacheSize)
		if err != nil {
			return nil, err
		}
	}

	return &Store{
		db:         db,
		tileStmt:   tileStmt,
		layerStmt:  layerStmt,
		statusStmt: statusStmt,
		cache:      cache,
	}, nil
}

func (s *Store) Tile(tileset string, tile *stores.Terrain) error {
	key := CacheKey{
		Tileset: tileset,
		Z:       tile.Z,
		X:       tile.X,
		Y:       tile.Y,
	}

	if s.cache != nil {
		if data, ok := s.cache.Get(key); ok {
			return tile.UnmarshalBinary(data)
		}
	}

	var data []byte

	err := s.tileStmt.QueryRow(tileset, tile.Z, tile.X, tile.Y).Scan(&data)
	if err == sql.ErrNoRows {
		return stores.ErrNoItem
	}
	if err != nil {
		return err
	}

	if s.cache != nil {
		s.cache.Add(key, data)
	}

	return tile.UnmarshalBinary(data)
}

func (s *Store) Layer(tileset string) ([]byte, error) {
	var data []byte

	err := s.layerStmt.QueryRow(tileset).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, stores.ErrNoItem
	}

	return data, err
}

func (s *Store) TilesetStatus(tileset string) stores.TilesetStatus {
	var exists int

	err := s.statusStmt.QueryRow(tileset).Scan(&exists)
	if err == sql.ErrNoRows {
		return stores.NOT_FOUND
	}
	if err != nil {
		return stores.NOT_SUPPORTED
	}

	return stores.FOUND
}
