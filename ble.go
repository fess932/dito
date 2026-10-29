package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
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

	btMu    sync.Mutex // к Дито обращаемся по очереди: цикл экрана и запросы из веб-интерфейса
	btReady bool

	rxMu    sync.Mutex
	rxBuf   []byte
	waiters = map[byte]chan []byte{}

	verbose bool // печатать все кадры от Дито (для разовых команд из консоли)

	btAddr string // адрес Дито: подключаемся сразу по нему; пусто - ищем по имени. Менять под btMu
)

var errNoReply = errors.New("Дито не ответил")

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
	if btAddr != "" {
		addr.Set(btAddr)
		dev, err = a.Connect(addr, bluetooth.ConnectionParams{})
	}
	if btAddr == "" || err != nil { // адрес не знаем или система его забыла - ищем в эфире
		if addr, err = btScan(a); err != nil {
			return err
		}
		if dev, err = a.Connect(addr, bluetooth.ConnectionParams{}); err != nil {
			return err
		}
		btAddr = addr.String()
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
	return tx.EnableNotifications(onNotify)
}

// btScan ищет Дито по имени, а если адрес уже известен - именно его, чтобы не уйти к соседнему Дито.
func btScan(a *bluetooth.Adapter) (addr bluetooth.Address, err error) {
	found := false
	t := time.AfterFunc(15*time.Second, func() { a.StopScan() })
	err = a.Scan(func(a *bluetooth.Adapter, r bluetooth.ScanResult) {
		if btAddr != "" && r.Address.String() == btAddr || btAddr == "" && strings.HasPrefix(r.LocalName(), name) {
			addr, found = r.Address, true
			a.StopScan()
		}
	})
	t.Stop()
	switch {
	case err != nil:
	case !found && btAddr != "":
		err = fmt.Errorf("%s с адресом %s не найден; чтобы искать любой Дито, уберите addr из настроек", name, btAddr)
	case !found:
		err = fmt.Errorf("%s не найден", name)
	}
	return addr, err
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

// btDo выполняет fn на открытом соединении; fresh - соединение только что открыто.
// Дито рвёт простаивающее соединение, поэтому при ошибке связи переподключаемся и повторяем один раз.
func btDo(fn func(fresh bool) error) (err error) {
	btMu.Lock()
	defer btMu.Unlock()
	for range 2 {
		fresh := false
		if !btReady {
			if err = btOpen(); err != nil {
				return err
			}
			btReady, fresh = true, true
		}
		if err = fn(fresh); err == nil || errors.Is(err, errNoReply) || errors.Is(err, errNoAlarm) {
			return err // нет ответа или нет ячейки - это не обрыв связи
		}
		btClose()
		btReady = false
	}
	return err
}

// btRequest шлёт команду и ждёт ответ Дито с тем же номером команды. Вызывать внутри btDo.
func btRequest(op byte, data ...byte) ([]byte, error) {
	ch := make(chan []byte, 1)
	rxMu.Lock()
	waiters[op] = ch
	rxMu.Unlock()
	defer func() {
		rxMu.Lock()
		delete(waiters, op)
		rxMu.Unlock()
	}()
	if err := btSend(frame(op, data...)); err != nil {
		return nil, err
	}
	select {
	case d := <-ch:
		return d, nil
	case <-time.After(time.Second):
		return nil, fmt.Errorf("0x%02x: %w", op, errNoReply)
	}
}

type devFrame struct {
	op, ack byte
	data    []byte
}

// splitFrames режет поток уведомлений на кадры Дито: 01 | len(LE16) | 04 | op | ack (55 = ок) | data | crc(LE16) | 02.
// len считает всё после 01, кроме crc и 02. Возвращает недочитанный хвост.
func splitFrames(buf []byte) ([]devFrame, []byte) {
	var out []devFrame
	for {
		i := bytes.IndexByte(buf, 0x01)
		if i < 0 {
			return out, nil
		}
		buf = buf[i:]
		if len(buf) < 3 {
			return out, buf
		}
		n := int(buf[1]) | int(buf[2])<<8
		// случайный 01 с огромной длиной иначе застопорит разбор: маркер 04 проверяем, не дожидаясь всего кадра
		if n < 4 || n > 4096 || len(buf) >= 4 && buf[3] != 0x04 {
			buf = buf[1:]
			continue
		}
		if len(buf) < n+4 {
			return out, buf
		}
		f := buf[:n+4]
		if f[len(f)-1] != 0x02 || f[3] != 0x04 {
			buf = buf[1:] // не начало кадра, ищем следующий 01
			continue
		}
		out = append(out, devFrame{f[4], f[5], bytes.Clone(f[6 : len(f)-3])})
		buf = buf[n+4:]
	}
}

func onNotify(b []byte) {
	if verbose {
		fmt.Printf("< % x\n", b)
	}
	rxMu.Lock()
	defer rxMu.Unlock()
	var frames []devFrame
	frames, rxBuf = splitFrames(append(rxBuf, b...))
	rxBuf = bytes.Clone(rxBuf)
	for _, f := range frames {
		if ch, ok := waiters[f.op]; ok {
			select {
			case ch <- f.data:
			default:
			}
		}
	}
}
