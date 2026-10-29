# dito

Часы, погода и любые картинки на экране **Divoom Ditoo Pro** (16×16) по Bluetooth LE, с веб-интерфейсом. Go, без cgo.

## Как устроен Ditoo Pro

У колонки две независимые части:

| Часть | Bluetooth | Для чего |
|---|---|---|
| `DitooPro-Audio` | классический | звук (A2DP, гарнитура) |
| `DitooPro-Light` | BLE | экран и команды |

Звук может быть подключён к одному компьютеру, а экраном управляет другой. Для BLE сопряжение не нужно, устройство ищется по имени.

Протокол: сервис UART от Microchip `49535343-fe7d-…`, запись с подтверждением в `49535343-8841-…`, ответы уведомлениями из `49535343-1e4d-…`. Пакет:

```
FE EF AA 55 | len (LE16) | 01 | номер пакета (LE32) | команда | данные | сумма (LE16)
```

`len` = команда + данные + 7, сумма считается по байтам от `len` до конца данных. Устройство подтверждает каждый пакет кадром `01 … 04 33 55 <номер> … 02`. Картинка: команда `0x44`, данные `00 0A 0A 04` и кадр `AA` с палитрой, индексы пикселей упакованы с младшего бита.

## Запуск на мини-ПК (Linux, podman)

```sh
sudo dnf install bluez podman podman-compose   # или apt
sudo systemctl enable --now bluetooth podman-restart
bluetoothctl power on

sudo mkdir -p /srv/composes/dito && cd /srv/composes/dito   # сюда compose.yaml из репы
sudo podman-compose up -d
```

Обновить на новый образ: podman-compose не пересоздаёт контейнер сам, поэтому

```sh
sudo podman-compose pull && sudo podman-compose down && sudo podman-compose up -d
```

Интерфейс: `http://<мини-ПК>:8080`. Настройки (режим, цвет, яркость, часовой пояс, город) хранятся в `./data/dito.json`.

Контейнер говорит с BlueZ хоста через `/run/dbus` и работает от root, поэтому podman нужен rootful (`sudo`). После перезагрузки контейнер поднимает `podman-restart.service`. С Docker тот же `compose.yaml` тоже работает.

Образ `ghcr.io/fess932/dito` собирается в GitHub Actions под amd64 и arm64 при каждом пуше в `main`.

## Команды

```sh
dito clock               # часы и погода раз в минуту + веб-интерфейс на $DITO_ADDR (:8080)
dito bright 0-100        # яркость
dito color ff0000        # залить экран цветом
dito preview out.png     # нарисовать кадр часов в PNG без устройства
dito raw 74 32           # любая команда: байт команды и данные в hex
```

## Скоро

Разобраны в чужих реверсах, но ещё не подключены: встроенные циферблаты, погода с прогнозом, бегущая строка встроенным шрифтом, анимации и GIF, таймер и секундомер, табло и шумомер, будильники и памятные даты, музыка (громкость, треки, SD-карта), подсветка клавиатуры, встроенные игры, синхронизация времени, батарея и версия прошивки, энергосбережение.

## Спасибо

Протокол взят из чужих реверсов:

- [mert4x4/ditoo-pro-custom-firmware](https://github.com/mert4x4/ditoo-pro-custom-firmware): конверт BLE и команды заводской прошивки
- [kirniy/divoom-ditoo-pro-mac](https://github.com/kirniy/divoom-ditoo-pro-mac): картинка 16×16 по BLE
- [d03n3rfr1tz3/hass-divoom](https://github.com/d03n3rfr1tz3/hass-divoom): команды и формат кадра
