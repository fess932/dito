package main

import (
	"bytes"
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
	ok := config{Mode: "clock", Color: "ff7800", Brightness: 50, TZ: "Europe/Belgrade"}
	if err := ok.validate(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []config{
		{Mode: "x", Color: "ff7800", TZ: "Local"},
		{Mode: "clock", Color: "fff", TZ: "Local"},
		{Mode: "clock", Color: "ff7800", Brightness: 101, TZ: "Local"},
		{Mode: "clock", Color: "ff7800", TZ: "Mars/Olympus"},
	} {
		if c.validate() == nil {
			t.Errorf("пропустил %+v", c)
		}
	}
}
