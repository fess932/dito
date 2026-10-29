package main

import (
	"bytes"
	"os"
	"slices"
	"testing"
)

func TestFrame(t *testing.T) {
	// яркость 100, первый пакет: собрано вручную по обоим реверсам
	seq = 0
	want := []byte{0xFE, 0xEF, 0xAA, 0x55, 0x09, 0x00, 0x01, 0x01, 0x00, 0x00, 0x00, 0x74, 0x64, 0xE3, 0x00}
	if got := frame(0x74, 100); !bytes.Equal(got, want) {
		t.Fatalf("got % x", got)
	}
}

func TestImage16(t *testing.T) {
	// первый пиксель красный, остальные чёрные: палитра из 2 цветов, 1 бит на пиксель, младший бит - первый пиксель
	var px [256]rgb
	for i := range px {
		px[i] = rgb{0, 0, 0}
	}
	px[0] = rgb{255, 0, 0}
	img := []byte{0xAA, 45, 0, 0, 0, 0, 2, 255, 0, 0, 0, 0, 0, 0xFE}
	img = append(img, bytes.Repeat([]byte{0xFF}, 31)...)
	seq = 0
	want := frame(0x44, append([]byte{0x00, 0x0A, 0x0A, 0x04}, img...)...)
	seq = 0
	if got := image16(&px); !bytes.Equal(got, want) {
		t.Fatalf("got  % x\nwant % x", got, want)
	}
}

func TestConfigValidate(t *testing.T) {
	ok := config{Mode: "clock", Color: "ff7800", Brightness: 50, TZs: []string{"Europe/Belgrade", "Local"}, TZ: "Europe/Belgrade",
		Cities: []string{"", "Belgrade"}, City: "Belgrade", ScreenSec: 60, WeatherMin: 10}
	if err := ok.validate(); err != nil {
		t.Fatal(err)
	}
	bad := func(f func(*config)) config { c := ok; f(&c); return c }
	for _, c := range []config{
		bad(func(c *config) { c.Mode = "x" }),
		bad(func(c *config) { c.Color = "fff" }),
		bad(func(c *config) { c.Brightness = 101 }),
		bad(func(c *config) { c.TZs = append(c.TZs, "Mars/Olympus") }),
		bad(func(c *config) { c.TZ = "Asia/Tokyo" }),
		bad(func(c *config) { c.City = "Paris" }),
		bad(func(c *config) { c.Cities = nil }),
		bad(func(c *config) { c.ScreenSec = 0 }),
		bad(func(c *config) { c.WeatherMin = 1 }),
	} {
		if c.validate() == nil {
			t.Errorf("пропустил %+v", c)
		}
	}
}

func TestLoadOldConfig(t *testing.T) {
	// файл от прошлой версии без периодов: настройки не сбрасываются, периоды берутся по умолчанию
	p := t.TempDir() + "/dito.json"
	os.WriteFile(p, []byte(`{"mode":"color","color":"00ff00","brightness":40,"tz":"Local","city":"Belgrade"}`), 0o644)
	c := loadState(p).cfg
	if c.Mode != "color" || c.City != "Belgrade" || c.ScreenSec != 60 || c.WeatherMin != 10 ||
		!slices.Equal(c.Cities, []string{"Belgrade"}) || !slices.Equal(c.TZs, []string{"Local"}) {
		t.Fatalf("%+v", c)
	}
}

func TestSplitFrames(t *testing.T) {
	// два настоящих ответа Дито, второй разрезан между уведомлениями, перед ними мусор
	a := []byte{0x01, 0x09, 0x00, 0x04, 0x33, 0x55, 0x01, 0x00, 0x00, 0x00, 0x96, 0x00, 0x02}
	b := []byte{0x01, 0x0d, 0x00, 0x04, 0xf7, 0x55, 0x4e, 0x6f, 0x62, 0x02, 0xc8, 0x03, 0x00, 0x00, 0x49, 0x03, 0x02}
	in := append(append([]byte{0xff, 0x01, 0x00}, a...), b[:5]...)
	got, rest := splitFrames(in)
	if len(got) != 1 || got[0].op != 0x33 || got[0].ack != 0x55 || !bytes.Equal(got[0].data, []byte{1, 0, 0, 0}) {
		t.Fatalf("first: %+v", got)
	}
	got, rest = splitFrames(append(rest, b[5:]...))
	if len(got) != 1 || got[0].op != 0xf7 || !bytes.Equal(got[0].data, b[6:len(b)-3]) || len(rest) != 0 {
		t.Fatalf("second: %+v rest % x", got, rest)
	}
}

func TestAlarmRoundTrip(t *testing.T) {
	a := alarm{Index: 2, On: true, Hour: 7, Minute: 30, Weekdays: 0b0111110, Mode: 1, Trigger: 3, FM: 1023, Volume: 60}
	if got := parseAlarms(a.payload()); len(got) != 1 || got[0] != a {
		t.Fatalf("%+v", got)
	}
}
