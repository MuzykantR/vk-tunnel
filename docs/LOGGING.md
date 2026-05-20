# Логи vk-vpn

## Формат

Одна метка времени на строку (в Go), без префикса `2026/05/19` от stdlib:

```
19:57:15.123 [creator] ICE connected
19:57:15.456 [dc] close id=4 64.233.161.102:443 tx=1204 rx=9832 active=3
```

В journald слева остаётся системная дата — дублирования нет.

## Переменные

| Переменная | Значение | Где |
|---|---|---|
| `VK_VPN_LOG_LEVEL` | `error`, `warn`, `info` (default), `debug` | Сервер (systemd), клиент (перед запуском) |
| `MASTER_KEY` / `MASTER_KEY_HEX` | 64 hex символа | Клиент, бот (см. parser/key.go) |
| `VK_VPN_VP8_FPS` / `VK_VPN_VP8_BATCH` | 24 / 30 | VP8 throughput pacing |
| `VK_VPN_VP8_PROFILE` | — / `aggressive` | Пресет **30×50** (если FPS/BATCH не заданы) |
| `VK_VPN_VP8_SEND_QUEUE` | 1024 | Глубина очереди VP8 (256–8192) |
| `VK_VPN_MTU` | 1400 | TUN + tun2socks (1280–1500) |
| `VK_VPN_ICE_RELAY_WAIT` | `3s` | Задержка перед выбором TURN relay |

## Уровни

- **info** — ICE, topology, participant, закрытие TCP (`dc close`)
- **debug** — каждый `transmitted-data`, открытие TCP (`dc open`), ICE candidates

## VP8: `[video] recv vp8 frame #N XX bytes`

Размер **XX** — длина собранного VP8 sample (после RTP), не скорость линии.

- **24–80 B** — чаще keepalive или `MsgConfig` (fps/batch), не ошибка.
- **KB+** — полезная нагрузка SOCKS (CONNECT/DATA); при нагрузке — каждый 500-й кадр + первые 5 + **все кадры ≥4096 B**.
- `vp8: sendQueue full, dropped` — очередь переполнена (фаза 1); увеличить `VK_VPN_VP8_SEND_QUEUE` или снизить batch.

Подробнее: [BENCHMARKS.md](BENCHMARKS.md).

## VP8 логи

Строка `[video] recv vp8 frame #N 70 bytes` (или `24`, `74` bytes) — **не ошибка** и не «пустой VPN».

Каждая строка = один **собранный VP8 RTP кадр** после расшифровки obfuscator, **до** разбора SOCKS-фреймов внутри:

| Размер (пример) | Содержимое |
|-----------------|------------|
| ~74 bytes | Первый кадр: часто **MsgConfig** (fps/batch) от joiner |
| ~24 bytes | **Keepalive** — пустой VP8 sample, чтобы SFU не засыпал |
| ~70 bytes | Keepalive или **мелкий control**-кадр |
| сотни–тысячи bytes | Реальный **SOCKS payload** (CONNECT/DATA) |

Лог печатается редко: кадры `#1–3`, потом каждый `#200` / `#500` / `#600` — при speedtest 18 Mbps большинство данных идёт в **крупных** кадрах, в journal видны в основном keepalive между burst’ами.

Полезный контроль: после connect должны быть `=== MODE: VIDEO (VP8) ===` и `peer vp8 config fps=24 batch=30`.

## TCP relay (`[dc]`)

- `open` — только в **debug** (страница открывает десятки параллельных запросов)
- `close id=N host tx=… rx=… active=…` — в **info**, одна строка на соединение
