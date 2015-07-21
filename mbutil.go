package main

import (
	"database/sql"
	"flag"
	"fmt"
	"github.com/j4/gosm"
	_ "github.com/mattn/go-sqlite3"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
)

const DEG_TO_RAD = math.Pi / 180
const RAD_TO_DEG = 180 / math.Pi
const MAX_LATITUDE = 85.0511287798
const EARTH_RADIUS = 6378137
const DEFAULT_TILE_SIZE = 256
const MAX_ZOOM_LEVEL = 19

func GetTileList(xmin float64, ymin float64, xmax float64, ymax float64, zoomlevel int) ([]*gosm.Tile, error) {
	t1 := gosm.NewTileWithLatLong(xmax, ymax, zoomlevel)
	t2 := gosm.NewTileWithLatLong(xmin, ymin, zoomlevel)
	tiles, err := gosm.BBoxTiles(*t1, *t2)
	return tiles, err
}

type Tile struct {
	z, x, y int
	Content []byte
}

func mbTileWorker(db *sql.DB, tilePipe chan Tile, outputPipe chan Tile) {
	for {
		tile := <-tilePipe
		err := addToMBTile(tile, db)
		if err != nil {
			log.Fatal(err)
		}
		outputPipe <- tile
	}
}

func addToMBTile(tile Tile, db *sql.DB) error {
	_, err := db.Exec("insert into tiles (zoom_level, tile_column, tile_row, tile_data) values (?, ?, ?, ?);", tile.z, tile.x, tile.y, tile.Content)
	if err != nil {
		return err
	}
	return nil
}

func tileFetcher(inputPipe chan Tile, tilePipe chan Tile) {
	for {
		tile := <-inputPipe
		tileObj := fetchTile(tile.z, tile.x, tile.y)
		tilePipe <- tileObj
	}
}

func fetchTile(z, x, y int) Tile {
	tile := Tile{}
	tile_url := getTileUrl(z, x, y)
	resp, err := http.Get(tile_url)
	if err != nil {
		log.Fatal("Error in fetching tile")
	}
	defer resp.Body.Close()
	tile.x = x
	tile.z = z
	tile.y = y
	tile.Content, err = ioutil.ReadAll(resp.Body)
	return tile
}

func getTileUrl(z, x, y int) string {
	url_format := "http://c.tile.openstreetmap.org/{z}/{x}/{y}.png"
	tile_url := strings.Replace(url_format, "{x}", strconv.Itoa(x), -1)
	tile_url = strings.Replace(tile_url, "{y}", strconv.Itoa(y), -1)
	tile_url = strings.Replace(tile_url, "{z}", strconv.Itoa(z), -1)
	return tile_url
}

func prepareDatabase(filename string) (*sql.DB, error) {
	os.Remove(filename)
	db, err := sql.Open("sqlite3", filename)
	if err != nil {
		return nil, err
	}

	err = optimizeConnection(db)
	if err != nil {
		return nil, err
	}
	return db, nil
}

func setupMBTileTables(db *sql.DB) error {

	_, err := db.Exec("create table if not exists tiles (zoom_level integer, tile_column integer, tile_row integer, tile_data blob);")
	if err != nil {
		return err
	}

	_, err = db.Exec("create table if not exists metadata (name text, value text);")
	if err != nil {
		return err
	}

	_, err = db.Exec("create unique index name on metadata (name);")
	if err != nil {
		return err
	}

	_, err = db.Exec("create unique index tile_index on tiles(zoom_level, tile_column, tile_row);")
	if err != nil {
		return err
	}

	return nil
}

func optimizeConnection(db *sql.DB) error {
	_, err := db.Exec("PRAGMA synchronous=0")
	if err != nil {
		return err
	}
	_, err = db.Exec("PRAGMA locking_mode=EXCLUSIVE")
	if err != nil {
		return err
	}
	_, err = db.Exec("PRAGMA journal_mode=DELETE")
	if err != nil {
		return err
	}
	return nil
}

func optimizeDatabase(db *sql.DB) error {
	_, err := db.Exec("ANALYZE;")
	if err != nil {
		return err
	}

	_, err = db.Exec("VACUUM;")
	if err != nil {
		return err
	}

	return nil
}

func main() {
	runtime.GOMAXPROCS(2)
	// xmin := 55.397945
	// ymin := 25.291090
	// xmax := 55.402741
	// ymax := 25.292889
	var xmin, ymin, xmax, ymax float64
	var zoomlevel int
	var filename string
	flag.Float64Var(&xmin, "xmin", 55.397945, "Minimum longitude")
	flag.Float64Var(&xmax, "xmax", 55.402741, "Maximum longitude")
	flag.Float64Var(&ymin, "ymin", 25.291090, "Minimum latitude")
	flag.Float64Var(&ymax, "ymax", 25.292889, "Maximum latitude")
	flag.StringVar(&filename, "filename", "/path/to/file.mbtile", "Output file to generate")
	flag.IntVar(&zoomlevel, "zoomlevel", 19, "Zoom level")
	flag.Parse()

	// nbtiles := math.Abs((float64(xmax))-float64(xmin)) + math.Abs(float64(ymax)-float64(ymin))
	// fmt.Println("Nbtiles ", nbtiles)

	// tiles, err := GetTileList(xmin, ymin, xmax, ymax, zoomlevel)
	proj := NewProjection(xmin, ymin, xmax, ymax, zoomlevel)
	tiles := proj.TileList()
	// if err != nil {
	// fmt.Println(err)
	// }
	fmt.Println("Number of tiles ", len(tiles))
	fmt.Println(tiles)

	db, err := prepareDatabase(filename)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	err = setupMBTileTables(db)
	if err != nil {
		log.Fatal(err)
	}

	inputPipe := make(chan Tile, len(tiles))
	tilePipe := make(chan Tile, len(tiles))
	outputPipe := make(chan Tile, len(tiles))

	for w := 0; w < 20; w++ {
		go tileFetcher(inputPipe, tilePipe)
	}

	for w := 0; w < 1; w++ {
		go mbTileWorker(db, tilePipe, outputPipe)
	}

	for _, tile := range tiles {
		inputPipe <- tile
	}

	// Waiting to complete the creation of db.
	for i := 0; i < len(tiles); i++ {
		<-outputPipe
	}

	err = optimizeDatabase(db)
	if err != nil {
		log.Fatal(err)
	}

}

func minMax(a, b, c float64) float64 {
	max_of_a_b := math.Max(a, b)
	return math.Min(max_of_a_b, c)
}

// func minMax(a, b, c int) int {
// 	var max_of_a_b, min int
// 	if a > b {
// 		max_of_a_b = a
// 	} else {
// 		max_of_a_b = b
// 	}

// 	if max_of_a_b < c {
// 		min = max_of_a_b
// 	} else {
// 		min = c
// 	}
// 	return min
// }

type Projection struct {
	Bc, Cc, Ac             []float64
	Zc                     [][]float64
	levels                 []int
	xmin, ymin, xmax, ymax float64
}

func NewProjection(xmin, ymin, xmax, ymax float64, zoomlevel int) *Projection {
	proj := Projection{xmin: xmin, ymin: ymin, xmax: xmax, ymax: ymax}
	for i := zoomlevel; i <= MAX_ZOOM_LEVEL; i++ {
		proj.levels = append(proj.levels, i)
	}

	var e float64
	var c = float64(DEFAULT_TILE_SIZE)
	for i := 0; i <= MAX_ZOOM_LEVEL; i++ {
		e = c / 2.0
		proj.Bc = append(proj.Bc, c/360.0)
		proj.Cc = append(proj.Cc, c/(2.0*math.Pi))
		proj.Zc = append(proj.Zc, []float64{e, e})
		proj.Ac = append(proj.Ac, c)
		c = c * 2
	}
	fmt.Println(proj.levels)
	return &proj
}

func (proj *Projection) project_pixels(x, y float64, zoom int) []float64 {
	fmt.Println(zoom)
	d := proj.Zc[zoom]
	e := Round(d[0] + x*proj.Bc[zoom])
	f := minMax(math.Sin(DEG_TO_RAD*y), -0.9999, 0.9999)
	g := Round(d[1] + 0.5*math.Log((1+f)/(1-f))*-proj.Cc[zoom])
	return []float64{e, g}
}

func (proj *Projection) TileList() []Tile {
	var tilelist []Tile

	for _, zoom := range proj.levels {
		two_power_zoom := math.Pow(2, float64(zoom))
		px0 := proj.project_pixels(proj.xmin, proj.ymax, zoom) // left top
		px1 := proj.project_pixels(proj.xmax, proj.ymin, zoom) // right bottom
		fmt.Println(px0, px1)
		xrangeStart := int(px0[0] / DEFAULT_TILE_SIZE)
		xrangeEnd := int(px1[0] / DEFAULT_TILE_SIZE)
		for x := xrangeStart; x <= xrangeEnd; x++ {
			if x < 0 || float64(x) > two_power_zoom {
				continue
			}
			yrangeStart := int(px0[1] / DEFAULT_TILE_SIZE)
			yrangeEnd := int(px1[1] / DEFAULT_TILE_SIZE)
			for y := yrangeStart; y <= yrangeEnd; y++ {
				if y < 0 || float64(y) > two_power_zoom {
					continue
				}
				tilelist = append(tilelist, Tile{z: zoom, x: x, y: y})
			}
		}

	}
	return tilelist
}

func Round(value float64) float64 {
	if value < 0 {
		return math.Ceil(value - 0.5)
	}
	return math.Floor(value + 0.5)
}
