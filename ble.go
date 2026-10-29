package main

import (
	"fmt"
	"strings"
	"time"

	"tinygo.org/x/bluetooth"
)

// Экраном управляет BLE-часть DitooPro-Light (UART Microchip), а не DitooPro-Audio: сопряжение не нужно.
const name = "DitooPro-Light"

var (
	uartSvc = mustUUID("49535343-fe7d-4ae5-8fa9-9fafd205e455")
	rxChr   = mustUUID("49535343-8841-43f4-a8d4-ecbe34729bb3") // запись в устройство
	txChr   = mustUUID("49535343-1e4d-4bd9-ba61-23c647249616") // ответы устройства
)

var (
	dev   bluetooth.Device
	rx    bluetooth.DeviceCharacteristic
	chunk = 20
)

func mustUUID(s string) bluetooth.UUID {
	u, err := bluetooth.ParseUUID(s)
	if err != nil {
		panic(err)
	}
	return u
}

func btOpen() (err error) {
	a := bluetooth.DefaultAdapter
	if err := a.Enable(); err != nil {
		return err
	}
	var addr bluetooth.Address
	found := false
	t := time.AfterFunc(15*time.Second, func() { a.StopScan() })
	err = a.Scan(func(a *bluetooth.Adapter, r bluetooth.ScanResult) {
		if strings.HasPrefix(r.LocalName(), name) {
			addr, found = r.Address, true
			a.StopScan()
		}
	})
	t.Stop()
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s не найден", name)
	}
	if dev, err = a.Connect(addr, bluetooth.ConnectionParams{}); err != nil {
		return err
	}
	defer func() { // не оставляем висеть полуоткрытое соединение
		if err != nil {
			dev.Disconnect()
		}
	}()
	svcs, err := dev.DiscoverServices([]bluetooth.UUID{uartSvc})
	if err != nil {
		return err
	}
	chars, err := svcs[0].DiscoverCharacteristics([]bluetooth.UUID{rxChr, txChr})
	if err != nil || len(chars) != 2 {
		return fmt.Errorf("характеристики UART: %v", err)
	}
	var tx bluetooth.DeviceCharacteristic
	rx, tx = chars[0], chars[1]
	if rx.UUID() != rxChr {
		rx, tx = tx, rx
	}
	if mtu, err := rx.GetMTU(); err == nil {
		chunk = max(20, min(509, int(mtu)-3))
	}
	return tx.EnableNotifications(func(b []byte) { fmt.Printf("< % x\n", b) })
}

// btSend пишет с подтверждением: без него характеристика запись не принимает.
func btSend(pkt []byte) error {
	for len(pkt) > 0 {
		n := min(chunk, len(pkt))
		if _, err := rx.Write(pkt[:n]); err != nil {
			return err
		}
		pkt = pkt[n:]
	}
	return nil
}

func btWait(sec float64) { time.Sleep(time.Duration(sec * float64(time.Second))) }

func btClose() { dev.Disconnect() }
