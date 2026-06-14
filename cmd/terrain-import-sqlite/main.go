package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

func main() {
	inputDir := flag.String("input", "", "input terrain root directory")
	tileset := flag.String("tileset", "", "tileset name")
	outputDb := flag.String("output", "", "output sqlite database path")
	description := flag.String("description", "", "optional tileset description")
	flag.Parse()

	if *inputDir == "" || *tileset == "" || *outputDb == "" {
		fmt.Println("usage:")
		fmt.Println("terrain-import-sqlite -input data -tileset SRTM1 -output terrain.sqlite")
		os.Exit(1)
	}

	tilesetDir := filepath.Join(*inputDir, *tileset)

	db, err := sql.Open("sqlite", *outputDb)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	_, err = db.Exec(`
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = NORMAL;

		CREATE TABLE IF NOT EXISTS tilesets (
			name TEXT PRIMARY KEY,
			layer_json BLOB NOT NULL,
			min_zoom INTEGER,
			max_zoom INTEGER,
			description TEXT
		);

		CREATE TABLE IF NOT EXISTS terrain_tiles (
			tileset TEXT NOT NULL,
			z INTEGER NOT NULL,
			x INTEGER NOT NULL,
			y INTEGER NOT NULL,
			data BLOB NOT NULL,
			PRIMARY KEY (tileset, z, x, y)
		);
	`)
	if err != nil {
		panic(err)
	}

	layerPath := filepath.Join(tilesetDir, "layer.json")
	layerJson, err := os.ReadFile(layerPath)
	if err != nil {
		panic(fmt.Errorf("failed to read layer.json: %w", err))
	}

	tx, err := db.Begin()
	if err != nil {
		panic(err)
	}

	_, err = tx.Exec(`
		INSERT OR REPLACE INTO tilesets(name, layer_json, description)
		VALUES (?, ?, ?)
	`, *tileset, layerJson, *description)
	if err != nil {
		tx.Rollback()
		panic(err)
	}

	stmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO terrain_tiles(tileset, z, x, y, data)
		VALUES (?, ?, ?, ?, ?)
	`)
	if err != nil {
		tx.Rollback()
		panic(err)
	}
	defer stmt.Close()

	count := 0
	minZoom := int(^uint(0) >> 1)
	maxZoom := -1

	err = filepath.WalkDir(tilesetDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() || !strings.HasSuffix(path, ".terrain") {
			return nil
		}

		rel, err := filepath.Rel(tilesetDir, path)
		if err != nil {
			return err
		}

		parts := strings.Split(rel, string(os.PathSeparator))
		if len(parts) != 3 {
			return nil
		}

		z, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil {
			return nil
		}

		x, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			return nil
		}

		yString := strings.TrimSuffix(parts[2], ".terrain")
		y, err := strconv.ParseUint(yString, 10, 64)
		if err != nil {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		_, err = stmt.Exec(*tileset, z, x, y, data)
		if err != nil {
			return err
		}

		zi := int(z)
		if zi < minZoom {
			minZoom = zi
		}
		if zi > maxZoom {
			maxZoom = zi
		}

		count++
		if count%1000 == 0 {
			fmt.Printf("imported %d tiles\n", count)
		}

		return nil
	})
	if err != nil {
		tx.Rollback()
		panic(err)
	}

	if count == 0 {
		tx.Rollback()
		panic("no .terrain files found")
	}

	_, err = tx.Exec(`
		UPDATE tilesets
		SET min_zoom = ?,
		    max_zoom = ?
		WHERE name = ?
	`, minZoom, maxZoom, *tileset)
	if err != nil {
		tx.Rollback()
		panic(err)
	}

	if err := tx.Commit(); err != nil {
		panic(err)
	}

	_, _ = db.Exec("ANALYZE")

	fmt.Printf(
		"done. imported %d terrain tiles for tileset %s, zooms %d-%d, into %s\n",
		count,
		*tileset,
		minZoom,
		maxZoom,
		*outputDb,
	)
}
