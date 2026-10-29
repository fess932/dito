package main

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

var errNoAlarm = errors.New("на Дито нет такой ячейки будильника")

// Команды чтения заводской прошивки по BLE, из mert4x4/ditoo-pro-custom-firmware (client/ditoo_api.py).

var channelNames = map[byte]string{0: "часы", 1: "свет", 2: "облако", 3: "VJ-эффекты", 4: "музыка", 5: "свои картинки", 6: "текст песни"}

// источник звука, как SOURCES в hass-divoom
var sourceNames = map[byte]string{0: "Bluetooth", 1: "FM-радио", 2: "AUX", 3: "SD-карта", 7: "USB"}

func yesNo(b byte) string {
	if b != 0 {
		return "да"
	}
	return "нет"
}

func name8(m map[byte]string, v byte) string {
	if s, ok := m[v]; ok {
		return s
	}
	return strconv.Itoa(int(v))
}

type probe struct {
	title, hint string
	op          byte
	arg         []byte
	min         int // сколько байт ответа нужно для разбора
	decode      func(d []byte) string
}

var probes = []probe{
	{"Текущий режим", "Канал, яркость, формат и стиль встроенных часов (ответ на 0x46).", 0x46, nil, 16, func(d []byte) string {
		h := "12 ч"
		if d[11] != 0 {
			h = "24 ч"
		}
		return fmt.Sprintf("канал: %s · яркость %d · %s · стиль часов %d", name8(channelNames, d[0]), d[10], h, d[15])
	}},
	{"Версия прошивки", "Номер сборки заводской прошивки.", 0x97, nil, 3, func(d []byte) string {
		return strconv.Itoa(int(d[1]) | int(d[2])<<8)
	}},
	{"Имя Bluetooth", "Суффикс имени, которое видят телефоны и компьютеры.", 0x76, nil, 1, func(d []byte) string {
		n := min(int(d[0]), len(d)-1)
		return strconv.Quote(string(d[1 : 1+n]))
	}},
	{"Единицы температуры", "В чём встроенный экран погоды показывает градусы.", 0x2B, []byte{0xFF}, 1, func(d []byte) string {
		if d[0] != 0 {
			return "°F"
		}
		return "°C"
	}},
	{"Энергосбережение", "Режим пониженного потребления.", 0xB3, nil, 1, func(d []byte) string { return yesNo(d[0]) }},
	{"Название песни на экране", "Показывать ли название трека при воспроизведении.", 0x83, []byte{0xFF}, 1, func(d []byte) string { return yesNo(d[0]) }},
	{"Экран реагирует на звук", "На Ditoo Pro команда принимается, но ничего не меняет.", 0xA8, nil, 1, func(d []byte) string { return yesNo(d[0]) }},
	{"Громкость при включении", "С какой громкостью Дито стартует после включения.", 0xBB, []byte{0, 0}, 1, func(d []byte) string { return strconv.Itoa(int(d[0])) }},
	{"Канал при включении", "Какой режим экрана открывается после включения.", 0x8A, []byte{0, 0}, 1, func(d []byte) string { return name8(channelNames, d[0]) }},
	{"Автовыключение", "Через сколько минут простоя Дито выключится сам; 0 - никогда.", 0xAC, nil, 2, func(d []byte) string {
		return strconv.Itoa(int(d[0])|int(d[1])<<8) + " мин"
	}},
	{"Источник звука", "Откуда сейчас играет звук.", 0x13, nil, 1, func(d []byte) string { return name8(sourceNames, d[0]) }},
	{"SD-карта", "Вставлена ли карта памяти.", 0x15, nil, 1, func(d []byte) string { return yesNo(d[0]) }},
	{"Пароль на подключение", "Нужен ли 4-значный пароль для Bluetooth.", 0x27, []byte{0x02}, 1, func(d []byte) string { return yesNo(d[0]) }},
	{"Громкость", "Текущая громкость, шкала 0-15.", 0x09, nil, 1, func(d []byte) string { return strconv.Itoa(int(d[0])) }},
	{"Музыка играет", "Идёт ли сейчас воспроизведение.", 0x0B, nil, 1, func(d []byte) string { return yesNo(d[0]) }},
	{"Будильники", "Сколько будильников включено из всех ячеек; подробно - в разделе будильников.", 0x42, nil, 0, func(d []byte) string {
		on, all := 0, parseAlarms(d)
		for _, a := range all {
			if a.On {
				on++
			}
		}
		return fmt.Sprintf("включено %d из %d", on, len(all))
	}},
	{"Расписание", "Включён ли план смены режимов по времени.", 0x57, []byte{0x00}, 1, func(d []byte) string { return yesNo(d[0]) }},
	{"Таймер сна", "Засыпание под звук: минуты, вкл/выкл, громкость.", 0xA2, nil, 10, func(d []byte) string {
		return fmt.Sprintf("%d мин · %s · громкость %d", d[0], map[bool]string{true: "включён", false: "выключен"}[d[2] != 0], d[5])
	}},
	{"Плеер SD-карты", "Позиция и длина трека, номер, режим повтора. Без карты не отвечает.", 0xB4, nil, 9, func(d []byte) string {
		return fmt.Sprintf("трек %d · %d из %d с · громкость %d · режим %d",
			int(d[4])|int(d[5])<<8, int(d[0])|int(d[1])<<8, int(d[2])|int(d[3])<<8, d[7], d[8])
	}},
	{"Треков на SD-карте", "Сколько файлов видит плеер. Без карты не отвечает.", 0x7D, nil, 2, func(d []byte) string {
		return strconv.Itoa(int(d[0]) | int(d[1])<<8)
	}},
}

type infoItem struct {
	Title string `json:"title"`
	Hint  string `json:"hint"`
	Op    string `json:"op"`
	Value string `json:"value,omitempty"`
	Raw   string `json:"raw,omitempty"`
	Error string `json:"error,omitempty"`
}

// readInfo опрашивает всё, что Дито отдаёт по BLE. Вызывать внутри btDo; нет ответа - это поле, а не ошибка связи.
func readInfo() ([]infoItem, error) {
	var out []infoItem
	for _, p := range probes {
		it := infoItem{Title: p.title, Hint: p.hint, Op: fmt.Sprintf("0x%02x", p.op)}
		d, err := btRequest(p.op, p.arg...)
		switch {
		case err != nil && !errors.Is(err, errNoReply):
			return nil, err
		case err != nil:
			it.Error = "не ответил"
		case len(d) < p.min:
			it.Error = "короткий ответ"
		default:
			it.Value = p.decode(d)
		}
		if d != nil {
			it.Raw = fmt.Sprintf("% x", d)
		}
		out = append(out, it)
	}
	return out, nil
}

type alarm struct {
	Index    int  `json:"index"`
	On       bool `json:"on"`
	Hour     int  `json:"hour"`
	Minute   int  `json:"minute"`
	Weekdays int  `json:"weekdays"` // бит 0 - воскресенье ... бит 6 - суббота; 0 - один раз
	Mode     int  `json:"mode"`     // звук будильника; из интерфейса не меняется
	Trigger  int  `json:"trigger"`  // что показывать при срабатывании; из интерфейса не меняется
	FM       int  `json:"fm"`       // частота радио, если будит радио
	Volume   int  `json:"volume"`
}

// parseAlarms разбирает ответ на 0x42: по 10 байт на ячейку.
func parseAlarms(d []byte) []alarm {
	var out []alarm
	for i := 0; i+10 <= len(d); i += 10 {
		r := d[i : i+10]
		out = append(out, alarm{int(r[0]), r[1] != 0, int(r[2]), int(r[3]), int(r[4]), int(r[5]), int(r[6]), int(r[7]) | int(r[8])<<8, int(r[9])})
	}
	return out
}

func (a alarm) payload() []byte {
	on := byte(0)
	if a.On {
		on = 1
	}
	return []byte{byte(a.Index), on, byte(a.Hour), byte(a.Minute), byte(a.Weekdays), byte(a.Mode), byte(a.Trigger), byte(a.FM), byte(a.FM >> 8), byte(a.Volume)}
}

func (a alarm) validate() error {
	switch {
	case a.Hour < 0 || a.Hour > 23 || a.Minute < 0 || a.Minute > 59:
		return fmt.Errorf("время будильника: часы 0-23, минуты 0-59")
	case a.Weekdays < 0 || a.Weekdays > 127:
		return fmt.Errorf("дни недели: битовая маска 0-127")
	case a.Volume < 0 || a.Volume > 100:
		return fmt.Errorf("громкость будильника: 0-100")
	}
	return nil
}

func readAlarms() ([]alarm, error) {
	d, err := btRequest(0x42)
	if err != nil {
		return nil, err
	}
	return parseAlarms(d), nil
}

// writeAlarm меняет одну ячейку: поля, которых нет в интерфейсе, берёт с самого Дито. Проверять in.validate() до btDo.
func writeAlarm(in alarm) ([]alarm, error) {
	all, err := readAlarms()
	if err != nil {
		return nil, err
	}
	for _, cur := range all {
		if cur.Index != in.Index {
			continue
		}
		in.Mode, in.Trigger, in.FM = cur.Mode, cur.Trigger, cur.FM
		if err := btSend(frame(0x43, in.payload()...)); err != nil {
			return nil, err
		}
		time.Sleep(300 * time.Millisecond) // Дито записывает ячейку не мгновенно
		return readAlarms()
	}
	return nil, fmt.Errorf("%d: %w", in.Index, errNoAlarm)
}

// syncTime выставляет часы самого Дито: по ним срабатывают будильники.
// 0x18: год%100, год/100, месяц, день, ч, мин, с, день недели (0 - воскресенье); Дито отвечает тем же 0x18.
// Прочитать часы обратно нельзя: в прошивке нет ответа на запрос времени (docs/opcodes.md в ditoo-pro-custom-firmware).
func syncTime(t time.Time) error {
	_, err := btRequest(0x18, byte(t.Year()%100), byte(t.Year()/100), byte(t.Month()), byte(t.Day()), byte(t.Hour()), byte(t.Minute()), byte(t.Second()), byte(t.Weekday()))
	return err
}
