package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"
)

// config - то, что меняется из веб-интерфейса; хранится в JSON-файле.
type config struct {
	Mode       string   `json:"mode"`       // clock: часы и погода, color: заливка цветом
	Color      string   `json:"color"`      // rrggbb
	Brightness int      `json:"brightness"` // 0-100
	TZs        []string `json:"tzs"`        // часовые пояса, между которыми можно переключаться
	TZ         string   `json:"tz"`         // активный: имя из базы IANA, Local - пояс системы
	Cities     []string `json:"cities"`     // города для погоды; пустая строка - по IP
	City       string   `json:"city"`       // активный город
	ScreenSec  int      `json:"screenSec"`  // как часто перерисовывать экран
	WeatherMin int      `json:"weatherMin"` // как часто спрашивать погоду
}

func (c config) validate() error {
	if c.Mode != "clock" && c.Mode != "color" {
		return fmt.Errorf("режим: clock или color")
	}
	if b, err := hex.DecodeString(c.Color); err != nil || len(b) != 3 {
		return fmt.Errorf("цвет: rrggbb")
	}
	if c.Brightness < 0 || c.Brightness > 100 {
		return fmt.Errorf("яркость: 0-100")
	}
	if len(c.TZs) == 0 || len(c.TZs) > 10 || len(c.Cities) == 0 || len(c.Cities) > 10 {
		return fmt.Errorf("в списках поясов и городов от 1 до 10 записей")
	}
	for _, tz := range c.TZs {
		if _, err := time.LoadLocation(tz); err != nil {
			return fmt.Errorf("часовой пояс %q не найден", tz)
		}
	}
	for _, city := range c.Cities {
		if len(city) > 100 {
			return fmt.Errorf("город %.20q...: слишком длинное название", city)
		}
	}
	if !slices.Contains(c.TZs, c.TZ) {
		return fmt.Errorf("активный пояс %q не из списка", c.TZ)
	}
	if !slices.Contains(c.Cities, c.City) {
		return fmt.Errorf("активный город %q не из списка", c.City)
	}
	if c.ScreenSec < 1 || c.ScreenSec > 3600 {
		return fmt.Errorf("обновление экрана: от 1 секунды до часа")
	}
	if c.WeatherMin < 5 || c.WeatherMin > 180 { // wttr.in ограничивает частые запросы
		return fmt.Errorf("обновление погоды: от 5 до 180 минут")
	}
	return nil
}

// state - общее состояние цикла отправки и веб-интерфейса.
type state struct {
	mu        sync.Mutex
	path      string
	cfg       config
	px        *[256]rgb
	w         weather
	connected bool
	err       string
	updated   time.Time
	kick      chan struct{}
}

func loadState(path string) *state {
	s := &state{path: path, kick: make(chan struct{}, 1), px: &[256]rgb{},
		cfg: config{Mode: "clock", Color: "ff7800", Brightness: 50, TZs: []string{"Local"}, TZ: "Local",
			Cities: []string{""}, ScreenSec: 60, WeatherMin: 10}}
	if b, err := os.ReadFile(path); err == nil {
		c := s.config() // поля, которых нет в старом файле, остаются по умолчанию
		c.TZs, c.Cities = nil, nil
		err := json.Unmarshal(b, &c)
		if len(c.TZs) == 0 { // файл до появления списков
			c.TZs = []string{c.TZ}
		}
		if len(c.Cities) == 0 {
			c.Cities = []string{c.City}
		}
		if err != nil || c.validate() != nil {
			fmt.Fprintln(os.Stderr, "настройки", path, "повреждены, беру по умолчанию")
		} else {
			s.cfg = c
		}
	}
	return s
}

// config отдаёт копию: списки не делят память с s.cfg, их можно менять и декодировать в них
func (s *state) config() config {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cfg
	c.TZs, c.Cities = slices.Clone(c.TZs), slices.Clone(c.Cities)
	return c
}

// setConfig сохраняет настройки (через временный файл, чтобы не испортить их при сбое) и будит цикл отправки.
func (s *state) setConfig(c config) error {
	if err := c.validate(); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(s.path+".tmp", b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(s.path+".tmp", s.path); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = c
	s.mu.Unlock()
	select {
	case s.kick <- struct{}{}:
	default:
	}
	return nil
}

// run раз в минуту (и сразу после смены настроек) рисует экран и шлёт его; соединение держим открытым и переподключаемся при обрыве.
func (s *state) run() {
	var fetched time.Time
	var fetchedCity string
	sentBright, syncedTZ := -1, ""
	for {
		cfg := s.config()
		if cfg.Mode == "clock" && (time.Since(fetched) > time.Duration(cfg.WeatherMin)*time.Minute || cfg.City != fetchedCity) {
			w, err := fetchWeather(cfg.City)
			if err != nil {
				fmt.Fprintln(os.Stderr, "погода:", err)
			}
			s.mu.Lock()
			if err == nil || cfg.City != fetchedCity { // на ошибке оставляем прошлую погоду, если город тот же
				s.w = w
			}
			w = s.w
			s.mu.Unlock()
			fetched, fetchedCity = time.Now(), cfg.City
		}

		loc, _ := time.LoadLocation(cfg.TZ)
		var px *[256]rgb
		if cfg.Mode == "color" {
			c, _ := hex.DecodeString(cfg.Color)
			px = &[256]rgb{}
			for i := range px {
				px[i] = rgb(c)
			}
		} else {
			s.mu.Lock()
			w := s.w
			s.mu.Unlock()
			px = render(time.Now().In(loc), w)
		}

		err := btDo(func(fresh bool) error {
			if fresh {
				sentBright, syncedTZ = -1, ""
			}
			if cfg.TZ != syncedTZ { // часы самого Дито нужны будильникам
				if err := syncTime(time.Now().In(loc)); err != nil {
					return err
				}
				syncedTZ = cfg.TZ
			}
			if cfg.Brightness != sentBright {
				if err := btSend(frame(0x74, byte(cfg.Brightness))); err != nil {
					return err
				}
				sentBright = cfg.Brightness
			}
			return btSend(image16(px))
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}

		s.mu.Lock()
		s.px, s.connected, s.updated, s.err = px, err == nil, time.Now(), ""
		if err != nil {
			s.err = err.Error()
		}
		s.mu.Unlock()

		period := time.Duration(cfg.ScreenSec) * time.Second
		select {
		case <-s.kick:
		case <-time.After(time.Until(time.Now().Truncate(period).Add(period))): // по границе периода: при 60 с - ровно в начале минуты
		}
	}
}

// fetchWeather берёт текущую погоду с wttr.in.
func fetchWeather(city string) (weather, error) {
	c := http.Client{Timeout: 10 * time.Second}
	resp, err := c.Get("https://wttr.in/" + url.PathEscape(city) + "?format=j1")
	if err != nil {
		return weather{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return weather{}, fmt.Errorf("wttr.in: %s", resp.Status)
	}
	var r struct {
		Current []struct {
			TempC       string `json:"temp_C"`
			WeatherCode string `json:"weatherCode"`
		} `json:"current_condition"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return weather{}, err
	}
	if len(r.Current) == 0 {
		return weather{}, fmt.Errorf("wttr.in: нет current_condition")
	}
	t, err := strconv.Atoi(r.Current[0].TempC)
	if err != nil {
		return weather{}, err
	}
	code, _ := strconv.Atoi(r.Current[0].WeatherCode)
	return weather{ok: true, temp: t, icon: iconFor(code)}, nil
}

// iconFor переводит код погоды WWO (его отдаёт wttr.in) в иконку.
func iconFor(code int) string {
	switch code {
	case 113:
		return "sun"
	case 116:
		return "partly"
	case 119, 122, 143, 248, 260:
		return "cloud"
	case 200, 386, 389, 392, 395:
		return "thunder"
	case 179, 227, 230, 323, 326, 329, 332, 335, 338, 350, 368, 371, 374, 377:
		return "snow"
	}
	return "rain"
}

// preview сохраняет то, что ушло бы на экран, в PNG (x20), чтобы проверить отрисовку без устройства.
func preview(path string) {
	w, err := fetchWeather("")
	if err != nil {
		fmt.Fprintln(os.Stderr, "погода:", err)
	}
	px := render(time.Now(), w)
	img := image.NewRGBA(image.Rect(0, 0, 320, 320))
	for y := range 320 {
		for x := range 320 {
			c := px[y/20*16+x/20]
			img.Set(x, y, color.RGBA{c[0], c[1], c[2], 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		fail(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fail(err)
	}
}
