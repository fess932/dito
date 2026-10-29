package main

import (
	"math/bits"
	"slices"
	"strconv"
	"time"
)

type rgb [3]byte

var (
	white  = rgb{255, 255, 255}
	orange = rgb{255, 120, 0}
	yellow = rgb{255, 200, 0}
	gray   = rgb{140, 140, 160}
	blue   = rgb{60, 120, 255}
	ice    = rgb{150, 220, 255}
)

// image16 кодирует картинку 16x16 в команду 0x44 (формат кадра из hass-divoom):
// AA | len(LE16) | time(LE16) | флаг палитры | число цветов (256 -> 0) | палитра RGB | индексы пикселей, упакованные с младшего бита.
func image16(px *[256]rgb) []byte {
	var pal []rgb
	idx := make([]int, len(px))
	for i, c := range px {
		j := slices.Index(pal, c)
		if j < 0 {
			j = len(pal)
			pal = append(pal, c)
		}
		idx[i] = j
	}
	nbits := max(1, bits.Len(uint(len(pal)-1)))
	img := []byte{0xAA, 0, 0, 0, 0, 0, byte(len(pal))}
	for _, c := range pal {
		img = append(img, c[:]...)
	}
	acc, n := 0, 0
	for _, j := range idx {
		acc |= j << n
		for n += nbits; n >= 8; n -= 8 {
			img = append(img, byte(acc))
			acc >>= 8
		}
	}
	if n > 0 {
		img = append(img, byte(acc))
	}
	img[1], img[2] = byte(len(img)), byte(len(img)>>8)
	return frame(0x44, append([]byte{0x00, 0x0A, 0x0A, 0x04}, img...)...)
}

var font = map[rune][5]string{
	'0': {"###", "#.#", "#.#", "#.#", "###"},
	'1': {".#.", "##.", ".#.", ".#.", "###"},
	'2': {"###", "..#", "###", "#..", "###"},
	'3': {"###", "..#", "###", "..#", "###"},
	'4': {"#.#", "#.#", "###", "..#", "..#"},
	'5': {"###", "#..", "###", "..#", "###"},
	'6': {"###", "#..", "###", "#.#", "###"},
	'7': {"###", "..#", "..#", "..#", "..#"},
	'8': {"###", "#.#", "###", "#.#", "###"},
	'9': {"###", "#.#", "###", "..#", "###"},
	'-': {"..", "..", "##", "..", ".."},
}

// иконки 5x5: буква - цвет, точка - фон
var icons = map[string][5]string{
	"sun":     {"y.y.y", ".yyy.", "yyyyy", ".yyy.", "y.y.y"},
	"partly":  {"y.y..", ".yy..", "y.ggg", ".gggg", "....."},
	"cloud":   {".....", ".ggg.", "ggggg", "ggggg", "....."},
	"rain":    {".ggg.", "ggggg", ".....", "b.b.b", ".b.b."},
	"snow":    {".ggg.", "ggggg", ".....", "i.i.i", ".i.i."},
	"thunder": {".ggg.", "ggggg", "..y..", ".y...", "..y.."},
}

var iconColors = map[byte]rgb{'y': yellow, 'g': gray, 'b': blue, 'i': ice}

type weather struct {
	ok   bool
	temp int
	icon string
}

func render(t time.Time, w weather) *[256]rgb {
	var px [256]rgb
	put := func(x, y int, c rgb) {
		if x >= 0 && x < 16 && y >= 0 && y < 16 {
			px[y*16+x] = c
		}
	}
	text := func(s string, x, y int, c rgb) {
		for _, r := range s {
			g := font[r]
			for dy, row := range g {
				for dx, p := range row {
					if p == '#' {
						put(x+dx, y+dy, c)
					}
				}
			}
			x += len(g[0]) + 1
		}
	}
	width := func(s string) (n int) {
		for _, r := range s {
			n += len(font[r][0]) + 1
		}
		return n - 1
	}

	// часы - цветом вместо двоеточия: на 16 пикселей HH:MM шрифтом 3x5 не влезает;
	// пары цифр по краям (x 0-6 и 9-15), как иконка и температура ниже
	text(t.Format("15"), 0, 1, white)
	text(t.Format("04"), 9, 1, orange)

	if !w.ok {
		text("--", 16-width("--"), 10, gray)
		return &px
	}
	for dy, row := range icons[w.icon] {
		for dx, p := range []byte(row) {
			if p != '.' {
				put(dx, 10+dy, iconColors[p])
			}
		}
	}
	s, c := strconv.Itoa(w.temp), yellow
	if w.temp <= 0 {
		c = ice
	}
	text(s, 16-width(s), 10, c)
	return &px
}
