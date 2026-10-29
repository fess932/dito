// dito: управление экраном Divoom Ditoo Pro по BLE.
package main

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
)

var seq uint32

// frame - конверт BLE (как в mert4x4/ditoo-pro-custom-firmware и kirniy/divoom-ditoo-pro-mac):
// FE EF AA 55 | len(LE16) | 01 | номер пакета (LE32) | cmd | data | sum(LE16); len = cmd+data+7, sum по len..data.
func frame(cmd byte, data ...byte) []byte {
	seq++
	n := len(data) + 8
	body := []byte{byte(n), byte(n >> 8), 0x01, byte(seq), byte(seq >> 8), byte(seq >> 16), byte(seq >> 24), cmd}
	body = append(body, data...)
	var sum uint16
	for _, b := range body {
		sum += uint16(b)
	}
	out := append([]byte{0xFE, 0xEF, 0xAA, 0x55}, body...)
	return append(out, byte(sum), byte(sum>>8))
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }

func main() {
	if len(os.Args) < 2 || len(os.Args) < 3 && os.Args[1] != "clock" {
		fmt.Println("usage: dito clock | preview <file.png> | bright <0-100> | color <rrggbb> | raw <cmd-hex> [data-hex]")
		fmt.Println("clock: часы и погода раз в минуту + веб-интерфейс на $DITO_ADDR (:8080), настройки в $DITO_CONFIG (dito.json)")
		os.Exit(2)
	}
	var pkt []byte
	switch os.Args[1] {
	case "clock":
		s := loadState(env("DITO_CONFIG", "dito.json"))
		go s.run()
		addr := env("DITO_ADDR", ":8080")
		fmt.Println("веб-интерфейс на", addr)
		fail(http.ListenAndServe(addr, s.handler()))
	case "preview":
		preview(os.Args[2])
		return
	case "bright":
		v, err := strconv.Atoi(os.Args[2])
		if err != nil || v < 0 || v > 100 {
			fail(fmt.Errorf("brightness 0-100"))
		}
		pkt = frame(0x74, byte(v))
	case "color":
		c, err := hex.DecodeString(os.Args[2])
		if err != nil || len(c) != 3 {
			fail(fmt.Errorf("color: rrggbb"))
		}
		var px [256]rgb
		for i := range px {
			px[i] = rgb(c)
		}
		pkt = image16(&px)
	case "raw":
		c, err := hex.DecodeString(os.Args[2])
		if err != nil || len(c) != 1 {
			fail(fmt.Errorf("cmd: один байт hex"))
		}
		var d []byte
		if len(os.Args) > 3 {
			if d, err = hex.DecodeString(os.Args[3]); err != nil {
				fail(err)
			}
		}
		pkt = frame(c[0], d...)
	default:
		fail(fmt.Errorf("unknown command %q", os.Args[1]))
	}

	if err := btOpen(); err != nil {
		fail(err)
	}
	defer btClose()
	fmt.Printf("> % x\n", pkt)
	if err := btSend(pkt); err != nil {
		fail(err)
	}
	btWait(1) // ждём ответ
}
