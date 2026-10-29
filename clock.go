package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// config - то, что меняется из веб-интерфейса; хранится в JSON-файле.
type config struct {
	Mode       string   `json:"mode"`           // clock: часы и погода, color: заливка цветом
	Color      string   `json:"color"`          // rrggbb
	Brightness int      `json:"brightness"`     // 0-100
	TZs        []string `json:"tzs"`            // часовые пояса, между которыми можно переключаться
	TZ         string   `json:"tz"`             // активный: имя из базы IANA, Local - пояс системы
	Cities     []string `json:"cities"`         // города для погоды; пустая строка - по IP
	City       string   `json:"city"`           // активный город
	ScreenSec  int      `json:"screenSec"`      // как часто перерисовывать экран
	WeatherMin int      `json:"weatherMin"`     // как часто спрашивать погоду
	HomeURL    string   `json:"homeURL"`        // метрики Prometheus с температурой в доме; пусто - только погода
	HomeMetric string   `json:"homeMetric"`     // какая строка метрик: name{label="value",...}
	Addr       string   `json:"addr,omitempty"` // адрес Дито, запоминается при первом подключении; убрать - искать заново
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
	if c.HomeURL != "" {
		if u, err := url.Parse(c.HomeURL); err != nil || u.Scheme != "http" && u.Scheme != "https" || len(c.HomeURL) > 300 {
			return fmt.Errorf("адрес метрик дома: http(s)://…")
		}
		if _, _, err := parseSelector(c.HomeMetric); err != nil {
			return err
		}
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
	home      weather // температура в доме, иконка home
	connected bool
	err       string
	updated   time.Time
	kick      chan struct{}
	syncTZ    string    // в каком поясе выставлены часы Дито; пусто - не выставлены с этого подключения
	syncAt    time.Time // когда Дито подтвердил время
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
	var homeAt time.Time
	var homeFrom string // для каких адреса и метрики s.home; пусто - дом не показываем
	sentBright := -1
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

		if cfg.HomeURL == "" {
			homeFrom = ""
		} else if time.Since(homeAt) > time.Minute || cfg.HomeURL+cfg.HomeMetric != homeFrom {
			h, err := homeTemp(cfg.HomeURL, cfg.HomeMetric)
			if err != nil {
				fmt.Fprintln(os.Stderr, "дом:", err)
			}
			s.mu.Lock()
			s.home = weather{ok: err == nil, temp: int(math.Round(h)), icon: "home"}
			s.mu.Unlock()
			homeAt, homeFrom = time.Now(), cfg.HomeURL+cfg.HomeMetric
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
			if homeFrom != "" && s.home.ok && time.Now().Unix()/60%2 == 1 { // по нечётным минутам - дом, по чётным - улица
				w = s.home
			}
			s.mu.Unlock()
			px = render(time.Now().In(loc), w)
		}

		var addr string
		err := btDo(func(fresh bool) error {
			addr = btAddr
			if fresh {
				sentBright = -1
				s.mu.Lock()
				s.syncTZ = ""
				s.mu.Unlock()
			}
			if err := s.syncDevice(cfg.TZ, false); err != nil {
				return err
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
		if err == nil && addr != cfg.Addr {
			c := s.config()
			c.Addr = addr
			if err := s.setConfig(c); err != nil {
				fmt.Fprintln(os.Stderr, "адрес Дито не сохранился:", err)
			}
		}

		s.mu.Lock()
		s.px, s.connected, s.updated, s.err = px, err == nil, time.Now(), ""
		if err != nil {
			s.err = err.Error()
		}
		s.mu.Unlock()

		period := time.Duration(cfg.ScreenSec) * time.Second
		wait := time.Until(time.Now().Truncate(period).Add(period)) // по границе периода: при 60 с - ровно в начале минуты
		if err != nil {
			wait = min(wait, 10*time.Second) // связь не поднялась - пробуем скоро, а не через период
		}
		select {
		case <-s.kick:
		case <-time.After(wait):
		}
	}
}

// syncDevice выставляет часы Дито в поясе tz, если они ещё не выставлены в нём (или force). Вызывать внутри btDo.
// Без ответа Дито время считаем не выставленным, но связь не рвём: попробуем при следующей отправке.
func (s *state) syncDevice(tz string, force bool) error {
	s.mu.Lock()
	done := s.syncTZ == tz
	s.mu.Unlock()
	if done && !force {
		return nil
	}
	loc, _ := time.LoadLocation(tz)
	err := syncTime(time.Now().In(loc))
	if errors.Is(err, errNoReply) {
		return nil
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.syncTZ, s.syncAt = tz, time.Now()
	s.mu.Unlock()
	return nil
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

var labelRe = regexp.MustCompile(`(\w+)="((?:[^"\\]|\\.)*)"`)

// parseSelector разбирает name{label="value",...} - и селектор из настроек, и строку метрик.
func parseSelector(s string) (string, map[string]string, error) {
	name, rest, braces := strings.Cut(strings.TrimSpace(s), "{")
	labels := map[string]string{}
	if braces {
		body, ok := strings.CutSuffix(rest, "}")
		if !ok {
			return "", nil, fmt.Errorf("метрика %q: нет закрывающей }", s)
		}
		for _, m := range labelRe.FindAllStringSubmatch(body, -1) {
			labels[m[1]] = m[2]
		}
	}
	if name == "" || strings.ContainsAny(name, " \t\"=,") {
		return "", nil, fmt.Errorf("метрика %q: нужно имя{метка=\"значение\"}", s)
	}
	return name, labels, nil
}

// homeTemp берёт из метрик Prometheus значение первой строки, подходящей под селектор.
func homeTemp(addr, selector string) (float64, error) {
	want, wantLabels, err := parseSelector(selector)
	if err != nil {
		return 0, err
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(addr)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s: %s", addr, resp.Status)
	}
	return matchMetric(resp.Body, want, wantLabels)
}

func matchMetric(r io.Reader, want string, wantLabels map[string]string) (float64, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 1<<20)
next:
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		i := strings.LastIndexByte(line, '}') + 1 // значение - после меток, а без меток - после имени
		if i == 0 {
			i = strings.IndexByte(line, ' ')
		}
		if i <= 0 {
			continue
		}
		name, labels, err := parseSelector(line[:i])
		if err != nil || name != want {
			continue
		}
		for k, v := range wantLabels {
			if labels[k] != v {
				continue next
			}
		}
		val, _, _ := strings.Cut(strings.TrimSpace(line[i:]), " ") // за значением может идти время
		return strconv.ParseFloat(val, 64)
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("в метриках нет %s%v", want, wantLabels)
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
